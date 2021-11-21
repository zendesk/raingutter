package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	log "github.com/sirupsen/logrus"
	"github.com/yalue/native_endian"
	"golang.org/x/sys/unix"
)

type ServerProcessCollection struct {
	Processes []*ServerProcess
}

type ServerProcess struct {
	Pid       int
	ProcDirFD *os.File
	UID       int
	GID       int

	// Identity
	IsMaster bool
	Index    int

	// Memory stats
	RSS  int
	PSS  int
	USS  int
	Anon int
}

// linuxDirent64 is linux_dirent64 from linux/dirent.h
type linuxDirent64 struct {
	DIno    uint64 // u64 d_ino
	DOff    int64  // s64 d_off
	DReclen uint16 // unsigned short d_reclen
	DType   uint8  // unsigned char d_type
	DName   byte   // The first byte of flexible array member char d_name[]
}

var unicornWorkerRegexp = regexp.MustCompile(`unicorn[\x00\x20]+worker\[([0-9]+)\]`)
var newMappingRegexp = regexp.MustCompile(`^([0-9a-f]+)-([0-9a-f]+)\s`)
var vSyscallRegexp = regexp.MustCompile(`\[vsyscall\]$`)
var anonymousRegexp = regexp.MustCompile(`^Anonymous:\s*([0-9]+)`)
var rssRegexp = regexp.MustCompile(`^Rss:\s*([0-9]+)`)
var pssRegexp = regexp.MustCompile(`^Pss:\s*([0-9]+)`)

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

	result = &ServerProcessCollection{}
	defer func() {
		// Ensure we don't leak pidFD's if we exit with an error.
		if errret != nil && result != nil {
			result.Close()
			result = nil
		}
	}()

	for _, entry := range procEntries {
		// use an IIFE so that we can defer closing the directory FD.
		func() {
			if !entry.IsDir() {
				return
			}
			pid, err := strconv.Atoi(entry.Name())
			if err != nil {
				// Not a proc/$PID directory
				return
			}
			dirFD, err := os.OpenFile(path.Join(procDir, entry.Name()), os.O_RDONLY|unix.O_DIRECTORY, 0)
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
			fdDirFD, err := unix.Openat(int(dirFD.Fd()), "fd", os.O_RDONLY|unix.O_DIRECTORY, 0)
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
				result.Processes = append(result.Processes, &ServerProcess{
					Pid:       pid,
					ProcDirFD: dirFD,
				})
				keepPidDirFD = true
			}
		}()
	}
	result.computeParentChildRelations(procDir)
	return result, nil
}

func (c *ServerProcessCollection) Close() {
	for _, proc := range c.Processes {
		proc.ProcDirFD.Close()
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
	fieldsThreeOnwards := strings.Split(statStr[endOfField2+2:len(statStr)], " ")
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
	procInodesToPids := make(map[uint64]int)
	for _, proc := range c.Processes {
		var statData unix.Stat_t
		err := unix.Fstat(int(proc.ProcDirFD.Fd()), &statData)
		if err == nil {
			procInodesToPids[statData.Ino] = proc.Pid
		}
	}

	// Figure out which processes are (direct or indirect) children of which other ones.
	// For every process, follow its parent chain until we get to PID1 or one of our existing
	// processes.
	for _, proc := range c.Processes {
		// This is a bit of a unicornism. We need a stable identifier for worker processes. We could use
		// the pid, but that has pretty high cardinality - ideally we'd like something that's like 1-N, for N
		// being the worker process count, that's also stable.
		// Unicorn helpfully actually includes a worker index in /proc/self/cmdline.
		cmdlineFD, err := unix.Openat(int(proc.ProcDirFD.Fd()), "cmdline", unix.O_RDONLY, 0)
		if err != nil {
			continue
		}
		cmdlineFile := os.NewFile(uintptr(cmdlineFD), "cmdline")
		cmdlineBytes, err := io.ReadAll(cmdlineFile)
		cmdlineFile.Close()
		if err != nil {
			continue
		}
		cmdline := string(cmdlineBytes)
		if strings.HasPrefix(cmdline, "unicorn") {
			if m := unicornWorkerRegexp.FindStringSubmatch(cmdline); m != nil {
				proc.Index, err = strconv.Atoi(m[1])
				if err != nil {
					continue
				}
			} else {
				proc.Index = -1
			}
		} else {
			// I'm just going to set this as zero for non-unicorn.
			// Setting this to a pid would "work", but it's too dangerous from an "accidentally emit lots of metrics"
			// perspective.
			proc.Index = 0
		}

		thisPid, err := parseParentPidFromProc(int(proc.ProcDirFD.Fd()))
		if err != nil {
			continue
		}

		// Parent pid of pid1 is 0, so that's our loop termination condition.
		pidIsDescendantOfAnotherPid := false
		for thisPid != 0 && !pidIsDescendantOfAnotherPid {
			func() {
				thisPidFD, err := os.OpenFile(path.Join(procDir, strconv.Itoa(thisPid)), unix.O_RDONLY|unix.O_DIRECTORY, 0)
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
				if _, has := procInodesToPids[statData.Ino]; has {
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
			proc.IsMaster = false
		} else {
			proc.IsMaster = true
		}

		// Read & save the UID/GID of the program - we need this later for memory stats.
		var statData unix.Stat_t
		err = unix.Fstat(int(proc.ProcDirFD.Fd()), &statData)
		if err != nil {
			continue
		}
		proc.UID = int(statData.Uid)
		proc.GID = int(statData.Gid)
	}
}

func (c *ServerProcessCollection) workerCount() int {
	ct := 0
	for _, proc := range c.Processes {
		if !proc.IsMaster {
			ct++
		}
	}
	return ct
}

func (c *ServerProcessCollection) collectMemoryStats(procDir string, hasCapSysAdmin bool) {
	for _, proc := range c.Processes {
		func() {
			mappingsFD, err := unix.Openat(int(proc.ProcDirFD.Fd()), "smaps", unix.O_RDONLY, 0)
			if err != nil {
				return
			}
			mappingsFile := os.NewFile(uintptr(mappingsFD), "smaps")
			defer mappingsFile.Close()

			// Parsing /proc/$pid/smaps is a real PITA.
			smapsScanner := bufio.NewScanner(mappingsFile)
			smapsScanner.Split(bufio.ScanLines)

			type mappingT struct {
				startAddr uint64
				endAddr   uint64
				anonBytes uint64
				rssBytes  uint64
				pssBytes  uint64
			}
			var mappings []*mappingT
			var thisMapping *mappingT
			for smapsScanner.Scan() {
				line := smapsScanner.Text()
				if m := newMappingRegexp.FindStringSubmatch(line); m != nil {
					thisMapping = &mappingT{}
					thisMapping.startAddr, err = strconv.ParseUint(m[1], 16, 64)
					if err != nil {
						log.Warnf("failed to parse /proc/%d/smaps: line %s", proc.Pid, line)
						return
					}
					thisMapping.endAddr, err = strconv.ParseUint(m[2], 16, 64)
					if err != nil {
						log.Warnf("failed to parse /proc/%d/smaps: line %s", proc.Pid, line)
						return
					}
					if !vSyscallRegexp.MatchString(line) {
						mappings = append(mappings, thisMapping)
					}
				} else if m := anonymousRegexp.FindStringSubmatch(line); m != nil {
					anonKb, err := strconv.ParseUint(m[1], 10, 64)
					if err != nil {
						log.Warnf("failed to parse /proc/%d/smaps: line %s", proc.Pid, line)
						return
					}
					thisMapping.anonBytes = anonKb * 1024
				} else if m := rssRegexp.FindStringSubmatch(line); m != nil {
					rssKb, err := strconv.ParseUint(m[1], 10, 64)
					if err != nil {
						log.Warnf("failed to parse /proc/%d/smaps: line %s", proc.Pid, line)
						return
					}
					thisMapping.rssBytes = rssKb * 1024
				} else if m := pssRegexp.FindStringSubmatch(line); m != nil {
					pssKb, err := strconv.ParseUint(m[1], 10, 64)
					if err != nil {
						log.Warnf("failed to parse /proc/%d/smaps: line %s", proc.Pid, line)
						return
					}
					thisMapping.pssBytes = pssKb * 1024
				}
			}

			// For each mapping, read the info out of /proc/$pid/pagemap
			pagemapFD, err := unix.Openat(int(proc.ProcDirFD.Fd()), "pagemap", unix.O_RDONLY, 0)
			if err != nil {
				return
			}
			pagemapFile := os.NewFile(uintptr(pagemapFD), "pagemap")
			defer pagemapFile.Close()

			if hasCapSysAdmin {
				// Set of kernel page frame numbers for every mapping we have, along with how many
				// times it's been mapped.
				residentKernelPfnCounts := map[uint64]int{}
				var residentKernelPfns []int
				for _, mapping := range mappings {
					startUserPfn := mapping.startAddr / uint64(os.Getpagesize())
					endUserPfn := mapping.endAddr / uint64(os.Getpagesize())
					buffer := make([]byte, (endUserPfn-startUserPfn)*8)
					_, err = pagemapFile.Seek(int64(startUserPfn*8), 0)
					if err != nil {
						return
					}
					n, err := pagemapFile.Read(buffer)
					if err != nil || n != len(buffer) {
						return
					}
					for i := 0; i < len(buffer); i += 8 {
						userPfnInfo := native_endian.NativeEndian().Uint64(buffer[i : i+8])

						isResident := (userPfnInfo & (0x1 << 63)) != 0
						// Kernel PFN data is in bits 0-53
						kernelPfn := userPfnInfo & ((0x1 << 53) - 1)
						if isResident && kernelPfn != 0 {
							// Put the pfn in the residentKernelPfns array once
							if residentKernelPfnCounts[kernelPfn] == 0 {
								residentKernelPfns = append(residentKernelPfns, int(kernelPfn))
							}
							// but keep a counter of how many times this page is mapped too
							residentKernelPfnCounts[kernelPfn] += 1
						}
					}
				}

				// Sort the kpfns into ranges
				sort.Ints(residentKernelPfns)
				type kpfnRange struct {
					start  int
					length int
				}
				var kpfnRanges []*kpfnRange
				thisKpfnRange := &kpfnRange{0, 0}
				for _, kpfn := range residentKernelPfns {
					if thisKpfnRange.start+thisKpfnRange.length < kpfn {
						thisKpfnRange = &kpfnRange{
							start:  kpfn,
							length: 1,
						}
						kpfnRanges = append(kpfnRanges, thisKpfnRange)
					} else {
						thisKpfnRange.length += 1
					}
				}

				kpageCountFile, err := os.OpenFile(path.Join(procDir, "kpagecount"), os.O_RDONLY, 0)
				if err != nil {
					// _this_ is unexpected
					log.Errorf("failed to open %s: %s", path.Join(procDir, "kpagecount"), err)
					return
				}
				defer kpageCountFile.Close()

				// Now look up all the mappings in /proc/kpagecount, and see if some _other_ process that's
				// not this one was _also_ contributing to the mapping count.
				proc.USS = 0
				for _, kpfnRange := range kpfnRanges {
					_, err := kpageCountFile.Seek(int64(kpfnRange.start*8), 0)
					if err != nil {
						continue
					}
					buffer := make([]byte, kpfnRange.length*8)
					n, err := kpageCountFile.Read(buffer)
					if err != nil || n != len(buffer) {
						continue
					}
					for i := 0; i < len(buffer); i += 8 {
						timesPageMapped := native_endian.NativeEndian().Uint64(buffer[i : i+8])
						kpfnNumber := uint64(kpfnRange.start + (i / 8))
						if int(timesPageMapped) <= residentKernelPfnCounts[kpfnNumber] {
							// This page must only be mapped in this process, so it contributes to
							// unique-set-size.
							proc.USS += os.Getpagesize()
						}
					}
				}
			}

			// Sum up values over all pages
			proc.RSS = 0
			proc.PSS = 0
			proc.Anon = 0
			for _, mapping := range mappings {
				proc.RSS += int(mapping.rssBytes)
				proc.PSS += int(mapping.pssBytes)
				proc.Anon += int(mapping.anonBytes)
			}
		}()
	}
}
