package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if os.Getenv("PROCESS_TEST__FORKED_CHILD") == "true" {
		// Chill and keep listening on the socket until we get killed.
		ctx, cancel := signal.NotifyContext(context.Background(), unix.SIGINT)
		defer cancel()
		<-ctx.Done()
		os.Exit(0)
	} else {
		os.Exit(m.Run())
	}
}

func TestFindProcessesListeningToSocket_SingleListener(t *testing.T) {
	// Open a listening socket.
	l, listenerInode := listenAndGetInodeNumber(t, "127.0.0.1:0")
	defer l.Close()

	// Find processes using that socket
	procs, err := FindProcessesListeningToSocket("/proc", listenerInode)
	if err != nil {
		t.Fatalf("error FindProcessesListeningToSocket: %s", err)
	}
	defer procs.Close()

	// This process should be the only process using that socket.
	if len(procs.Processes) != 1 {
		t.Fail()
	}
	if procs.Processes[0].Pid != os.Getpid() {
		t.Fail()
	}
}

func TestFindProcessesListeningToSocket_Forked(t *testing.T) {
	// Open a listening socket.
	l, listenerInode := listenAndGetInodeNumber(t, "127.0.0.1:0")
	defer l.Close()
	lFile, err := l.File()
	if err != nil {
		t.Fatalf("l.File(): %s", err)
	}

	childCmd := exec.Command("/proc/self/exe")
	childCmd.Env = append(childCmd.Env, "PROCESS_TEST__FORKED_CHILD=true")
	childCmd.ExtraFiles = append(childCmd.ExtraFiles, lFile)
	runC := make(chan error)
	go func() {
		runC <- childCmd.Run()
	}()

	// Wait for a bit until there is one master (us) and one worker (the child)
	testPassed := false
	for i := 0; i < 10; i++ {
		procs, err := FindProcessesListeningToSocket("/proc", listenerInode)
		if err != nil {
			t.Fatalf("error FindProcessesListeningToSocket: %s", err)
		}

		testPassed = func() bool {
			procsByPid := map[int]*ServerProcess{}
			for _, proc := range procs.Processes {
				procsByPid[proc.Pid] = proc
			}

			t.Logf("PROCS: %+v", procs.Processes)

			if len(procs.Processes) != 2 {
				return false
			}
			masterProc, has := procsByPid[os.Getpid()]
			if !has {
				return false
			}
			if !masterProc.IsMaster {
				return false
			}
			workerProc, has := procsByPid[childCmd.Process.Pid]
			if !has {
				return false
			}
			if workerProc.IsMaster {
				return false
			}
			return true
		}()
		if testPassed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	syscall.Kill(childCmd.Process.Pid, unix.SIGINT)
	<-runC

	if !testPassed {
		t.Fail()
	}
}
