// Author: Sean Goedecke <sgoedecke@zendesk.com>

package main

import (
	"errors"
	"io/ioutil"
	"path"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

type SocketStats struct {
	QueueSize     uint64
	ActiveWorkers uint64
	ListenerInode uint64
}

type Socket struct {
	LocalPort int64
	ConnState string
	Inode     string
	QueueSize uint64
}

// strip out the `sl local_address remote_address...` menu and trailing whitespace
func stripMenu(s string) string {
	sc := strings.SplitAfterN(string(s), "\n", 2)
	if len(sc) == 1 {
		return ""
	} else {
		return strings.TrimSpace(sc[1])
	}
}

// GetSocketStats combines the tcp and tcp6 /proc/net files to get a list of all ipv4 and ipv6 sockets
func GetSocketStats(procDir string) (string, error) {
	s6, err := ioutil.ReadFile(path.Join(procDir, "net/tcp6"))
	if err != nil {
		return "", err
	}
	ipv6sockets := stripMenu(string(s6))

	s4, err := ioutil.ReadFile(path.Join(procDir, "net/tcp"))
	if err != nil {
		return "", err
	}
	ipv4sockets := stripMenu(string(s4))
	sockets := ipv4sockets + "\n" + ipv6sockets

	return sockets, nil
}

// ParseSocket parses a line of /proc/net/tcp and returns a struct with some relevant info
// reference: https://www.kernel.org/doc/Documentation/networking/proc_net_tcp.txt
func ParseSocket(s string) (Socket, error) {
	fields := strings.Fields(s)
	if len(fields) < 9 {
		return Socket{}, errors.New("could not parse socket - too few fields: " + s)
	}

	inode := fields[9]

	localAddr := fields[1] // ipv4addr:port
	lp := strings.Split(localAddr, ":")
	if len(lp) < 2 {
		return Socket{}, errors.New("could not parse socket local address: " + localAddr)
	}
	localPort, err := strconv.ParseInt(lp[1], 16, 0)
	if err != nil {
		return Socket{}, err
	}

	// https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/tree/include/net/tcp_states.h#n12
	stateCode := fields[3]
	var connState string
	switch stateCode {
	case "0A":
		connState = "LISTEN"
	case "01":
		connState = "ESTAB"
	case "06":
		connState = "TIME-WAIT"
	default:
		connState = ""
	}

	qs := strings.Split(fields[4], ":") // transmit-queue:receive-queue
	if len(qs) < 2 {
		return Socket{}, errors.New("could not parse socket queue size: " + fields[6])
	}
	queueSize, err := strconv.ParseInt(qs[1], 16, 0)
	if err != nil {
		return Socket{}, err
	}

	return Socket{localPort, connState, inode, uint64(queueSize)}, nil

}

func ParseSocketStats(serverPort string, ssOutput string) (*SocketStats, error) {
	port, err := strconv.Atoi(serverPort)
	if err != nil {
		return nil, err
	}

	var queueSize uint64
	var activeWorkers uint64
	var listenerInode uint64

	sockets := strings.Split(ssOutput, "\n")
	for _, s := range sockets {
		if s == "" {
			continue
		}

		socket, err := ParseSocket(s)
		if err != nil {
			log.Error(err)
			continue
		}

		// we only want sockets on our port (to filter for Unicorn sockets)
		// we also ignore TIME-WAIT sockets - they've been handed off to the kernel
		// to sit on ice, Unicorn no longer cares
		if int(socket.LocalPort) != port || socket.ConnState == "TIME-WAIT" {
			continue
		}

		// for the single LISTEN socket that all Unicorn workers poll on, look
		// at the Recv-Q size as a measure of queue depth
		if socket.ConnState == "LISTEN" {
			queueSize = socket.QueueSize
			listenerInodeVal, err := strconv.ParseUint(socket.Inode, 10, 64)
			if err == nil {
				listenerInode = listenerInodeVal
			}
		}

		// sockets in the ESTAB state are short-lived sockets that represent a request
		// being handled by a single Unicorn process - we count these as a measure of
		// active workers
		// some ESTAB sockets have an inode of 0 - pretty sure these represent finished
		// connections in the process of being handed off to the kernel to keep on
		// TIME-WAIT. we assume Unicorn no longer cares about these and ignore them
		if socket.ConnState == "ESTAB" && socket.Inode != "0" {
			activeWorkers++
		}
	}

	return &SocketStats{queueSize, activeWorkers, listenerInode}, nil
}
