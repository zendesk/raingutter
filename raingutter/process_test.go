package main

import (
	"os"
	"testing"
)

func TestFindProcessesListeningToSocket(t *testing.T) {
	// Open a listening socket.
	l, listenerInode := listenAndGetInodeNumber(t, "127.0.0.1:0")
	defer l.Close()

	// Find processes using that socket
	procs, err := FindProcessesListeningToSocket("/proc", listenerInode)
	if err != nil {
		t.Fatalf("error FindProcessesListeningToSocket: %s", err)
	}

	// This process should be the only process using that socket.
	if len(procs) != 1 {
		t.Fail()
	}
	if procs[0].Pid != os.Getpid() {
		t.Fail()
	}
}
