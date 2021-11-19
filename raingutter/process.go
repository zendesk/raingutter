package main

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

type ServerProcess struct {
	Pid int
}

// linuxDirent64 is linux_dirent64 from linux/dirent.h
type linuxDirent64 struct {
	DIno uint64 // u64 d_ino
	DOff int64 // s64 d_off
	DReclen uint16 // unsigned short d_reclen
	DType uint8 // unsigned char d_type
	DName byte // The first byte of flexible array member char d_name[]
}

func FindProcessesListeningToSocket(procDir string, socketInode uint64) ([]ServerProcess, error) {
	// Check what network namespace _we_ are in.
	selfNetNs, err := os.Readlink(path.Join(procDir, "self/ns/net"))
	if err != nil {
		return nil, fmt.Errorf("error reading %s/self/ns/net: %w", procDir, err)
	}

	// List out every process in /proc
	procEntries, err := os.ReadDir(procDir)
	if err != nil {
		return nil, fmt.Errorf("error reading dir %s: %w", procDir, err)
	}

	var result []ServerProcess

	for _, entry := range procEntries {
		// use an IIFE so that we can defer closing the directory FD.
		func(){
			if !entry.IsDir() {
				return
			}
			pid, err := strconv.Atoi(entry.Name())
			if err != nil {
				// Not a proc/$PID directory
				return
			}
			dirFD, err := os.OpenFile(path.Join(procDir, entry.Name()), os.O_RDONLY | unix.O_DIRECTORY, 0)
			if err != nil {
				// failed to open the directory - process may simply have died before we could open it.
				return
			}
			defer dirFD.Close()

			// Is this process in the same namespace as us?
			// format of this link is "net:[uint64 in base10]" so this buffer is always
			// going to be large enough
			linkBuffer := make([]byte, 64)
			n, err := unix.Readlinkat(int(dirFD.Fd()), "ns/net", linkBuffer)
			if err != nil {
				// Could mean a couple of things - this process might have exited, or we might not
				// be in the same (or parent) net namespace as the pid. We want to filter for processes
				// in the same network namespace anyway, so ignore this regardless.
				return
			}
			pidNetNs := string(linkBuffer[:n])
			if selfNetNs != pidNetNs {
				// this process is in a different network namespace. Ignore.
				return
			}


			// See if it has an open file with the given socketInode.
			fdDirFD, err := unix.Openat(int(dirFD.Fd()), "fd", os.O_RDONLY | unix.O_DIRECTORY, 0)
			if err != nil {
				// could have exited
				return
			}
			defer unix.Close(fdDirFD)

			pidHasListenerSocketOpened := false
			// Gross-time. Golang has no binding for "read entries from a directory FD". This is important,
			// because pids can be recycled, so doing something like os.Readdir("/proc/$pid/fd") is racey.
			// Linux has the getdents64 syscall for this purpose; use the raw syscall API.
			dentBuf := make([]byte, 4096)
			for {
				nBytes, err := unix.Getdents(fdDirFD, dentBuf)
				if err != nil {
					return
				}
				if nBytes == 0 {
					break
				}
				offset := 0
				for offset < nBytes {
					dentStruct := (*linuxDirent64)(unsafe.Pointer(&dentBuf[offset]))

					fdFileNameStartOffset := offset + int(unsafe.Offsetof(dentStruct.DName))
					fdFileNameEndOffset := fdFileNameStartOffset
					for dentBuf[fdFileNameEndOffset] != 0 {
						fdFileNameEndOffset++
					}
					fdFileName := string(dentBuf[fdFileNameStartOffset:fdFileNameEndOffset])

					linkBytes, err := unix.Readlinkat(fdDirFD, fdFileName, linkBuffer)
					if err == nil && string(linkBuffer[:linkBytes]) == fmt.Sprintf("socket:[%d]", socketInode) {
						// This process has the listener socket opened.
						pidHasListenerSocketOpened = true
					}

					offset += int(dentStruct.DReclen)
				}
			}

			if pidHasListenerSocketOpened {
				result = append(result, ServerProcess{
					Pid: pid,
				})
			}
		}()
	}
	return result, nil
}
