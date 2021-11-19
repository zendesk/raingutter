package main

import (
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

type ServerProcess struct {
	Pid int
}

type ServerProcessCollection struct {
	ProcDirFDs map[int]*os.File
	ProcInodesToPids map[uint64]int
	MasterPids []int
	WorkerPids []int
}

// linuxDirent64 is linux_dirent64 from linux/dirent.h
type linuxDirent64 struct {
	DIno uint64 // u64 d_ino
	DOff int64 // s64 d_off
	DReclen uint16 // unsigned short d_reclen
	DType uint8 // unsigned char d_type
	DName byte // The first byte of flexible array member char d_name[]
}

func FindProcessesListeningToSocket(procDir string, socketInode uint64) (result *ServerProcessCollection, errret error) {
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

	result = &ServerProcessCollection{
		ProcDirFDs: map[int]*os.File{},
	}
	defer func() {
		// Ensure we don't leak pidFD's if we exit with an error.
		if errret != nil && result != nil {
			result.Close()
			result = nil
		}
	}()


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
			keepPidDirFD := false
			defer func() {
				if !keepPidDirFD {
					dirFD.Close()
				}
			}()

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
				result.ProcDirFDs[pid] = dirFD
				keepPidDirFD = true
			}
		}()
	}
	result.computeParentChildRelations(procDir)
	return result, nil
}

func (c *ServerProcessCollection) Close() {
	for _, fd := range c.ProcDirFDs {
		fd.Close()
	}
}

func parseParentPidFromProc(procDirFd int) (int, error) {
	statFD, err := unix.Openat(procDirFd, "stat", unix.O_RDONLY, 0)
	if err != nil {
		return -1, fmt.Errorf("openat stat: %w", err)
	}
	statFile := os.NewFile(uintptr(statFD), "stat")
	defer statFile.Close()
	statData, err := io.ReadAll(statFile)
	if err != nil {
		return -1, fmt.Errorf("read stat: %w", err)
	}

	// Because field 2 of /proc/pid/stat contains an unescaped copy of the program name,
	// to reliably parse the file you actually have to count backwards from the end, and
	// find the first ) character, to identify the end of field #2.
	// We're looking for PPID, which is field #4 (one-based index).
	statStr := string(statData)
	endOfField2 := strings.LastIndex(statStr, ")")
	// Everything after endOfField2 can just be string.split'd
	fieldsThreeOnwards := strings.Split(statStr[endOfField2 + 2:len(statStr)], " ")
	if len(fieldsThreeOnwards) < 2 {
		return -1, fmt.Errorf("malformed stat file: field count")
	}
	parentPid, err := strconv.Atoi(fieldsThreeOnwards[1])
	if err != nil {
		return -1, fmt.Errorf("malformed stat file: parent pid")
	}
	return parentPid, nil
}

func (c *ServerProcessCollection) computeParentChildRelations(procDir string) {
	// Build a map of proc inodes -> pid so we can answer "are these two processes the same"
	c.ProcInodesToPids = make(map[uint64]int)
	for pid, dirfd := range c.ProcDirFDs {
		var statData unix.Stat_t
		err := unix.Fstat(int(dirfd.Fd()), &statData)
		if err == nil {
			c.ProcInodesToPids[statData.Ino] = pid
		}
	}

	// Figure out which processes are (direct or indirect) children of which other ones.
	// For every process, follow its parent chain until we get to PID1 or one of our existing
	// processes.
	for pid, dirfd := range c.ProcDirFDs {
		thisPid, err := parseParentPidFromProc(int(dirfd.Fd()))
		if err != nil {
			continue
		}

		// Parent pid of pid1 is 0, so that's our loop termination condition.
		pidIsDescendantOfAnotherPid := false
		for thisPid != 0 && !pidIsDescendantOfAnotherPid {
			func(){
				thisPidFD, err := os.OpenFile(path.Join(procDir, strconv.Itoa(thisPid)), unix.O_RDONLY | unix.O_DIRECTORY, 0)
				if err != nil {
					// process could have exited.
					thisPid = 0
					return
				}
				defer thisPidFD.Close()

				var statData unix.Stat_t
				err = unix.Fstat(int(thisPidFD.Fd()), &statData)
				if err != nil {
					thisPid = 0
					return
				}
				if _, has := c.ProcInodesToPids[statData.Ino]; has {
					// The process `pid` is a (direct or indirect) child of another process in `c.ProcDirFds`.
					pidIsDescendantOfAnotherPid = true
					return
				}

				thisPid, err = parseParentPidFromProc(int(thisPidFD.Fd()))
				if err != nil {
					thisPid = 0
					return
				}
			}()
		}

		if pidIsDescendantOfAnotherPid {
			c.WorkerPids = append(c.WorkerPids, pid)
		} else {
			c.MasterPids = append(c.MasterPids, pid)
		}
	}
}
