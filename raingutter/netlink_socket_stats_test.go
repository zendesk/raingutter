package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

func testNetlinkSocketStatsImpl(t *testing.T, bindAddr string) {
	// Set up a listener socket
	l, err := net.Listen("tcp", bindAddr)
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	listenerFd, err := l.(*net.TCPListener).File()
	if err != nil {
		t.Fatalf("failed to get fd for listener: %s", err)
	}
	listenerInodeStr, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", listenerFd.Fd()))
	if err != nil {
		t.Fatalf("failed to readlink fd for listener: %s", err)
	}
	socketIndoeRegexp := regexp.MustCompile(`socket:\[([0-9]+)\]`)
	matches := socketIndoeRegexp.FindStringSubmatch(listenerInodeStr)
	if len(matches) < 2 {
		t.Fatalf("could not parse socket inode %s", listenerInodeStr)
	}
	listenerInode, err := strconv.Atoi(matches[1])
	if err != nil {
		t.Fatalf("could not convert socket inode %s to int: %s", matches[1], err)
	}

	rnlc, err := NewRaingutterNetlinkConnection()
	if err != nil {
		t.Fatalf("failed to create netlink connection: %s", err)
	}
	defer rnlc.Close()

	// Check the inode on the listener socket
	stats, err := rnlc.ReadStats(port)
	if err != nil {
		t.Fatalf("failed to read stats: %s", err)
	}
	if stats.ListenerInode != uint64(listenerInode) {
		t.Fail()
	}
	// There should be nothing connected to it yet.
	if stats.QueueSize != 0 || stats.ActiveWorkers != 0 {
		t.Fail()
	}

	// Connect to it. Should be one queued, zero active.
	go func() {
		conn, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Errorf("dial failure: %s", err)
			return
		}
		defer conn.Close()
		_, _ = io.ReadAll(conn)
	}()
	// Loop a copule times waiting for `net.Dial` in the goroutine to _actually_ open
	// the connection
	didGetQueuedConnection := false
	for i := 0; i < 5; i++ {
		stats, err := rnlc.ReadStats(port)
		if err != nil {
			t.Fatalf("failed to read stats: %s", err)
		}
		if stats.QueueSize == 1 && stats.ActiveWorkers == 0 {
			didGetQueuedConnection = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !didGetQueuedConnection {
		t.Fail()
	}

	// Now accept the connection.
	acceptedConn, err := l.Accept()
	if err != nil {
		t.Fatalf("accept error: %s", err)
	}
	// Should be one active, zero queued
	stats, err = rnlc.ReadStats(port)
	if err != nil {
		t.Fatalf("failed to read stats: %s", err)
	}
	if stats.QueueSize != 0 || stats.ActiveWorkers != 1 {
		t.Fail()
	}

	// Close it. Should be zero/zero again
	acceptedConn.Close()

	didGetZeroConnections := false
	for i := 0; i < 5; i++ {
		stats, err := rnlc.ReadStats(port)
		if err != nil {
			t.Fatalf("failed to read stats: %s", err)
		}
		if stats.QueueSize == 0 && stats.ActiveWorkers == 0 {
			didGetZeroConnections = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !didGetZeroConnections {
		t.Fail()
	}
}

func TestNetlinkSocketStatsIPv4(t *testing.T) {
	testNetlinkSocketStatsImpl(t, "127.0.0.1:0")
}

func TestNetlinkSocketStatsIPv6(t *testing.T) {
	testNetlinkSocketStatsImpl(t, "[::1]:0")
}
