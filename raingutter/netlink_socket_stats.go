package main

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// First up, we need to copy some bits out of the kernel headers and translate them
// into Go.

// NetlinkSockDiag is NETLINK_SOCK_DIAG from uapi/linux/netlink.h
const NetlinkSockDiag int = 4

// SockDiagByFamily is SOCK_DIAG_BY_FAMILY from uapi/linux/inet_diag.h
const SockDiagByFamily netlink.HeaderType = 20

// InetDiagReqV2 is the struct inet_diag_req_v2 from uapi/linux/inet_diag.h
type InetDiagReqV2 struct {
	SdiagFamily   uint8     // __u8 sdiag_family
	SdiagProtocol uint8     // __u8 sdiag_protocol
	IdiagExt      uint8     // __u8 idiag_ext
	Pad           uint8     // __u8 pad
	IdaigStates   uint32    // __u32 idiag_states
	IdiagSport    [2]byte   // __be16 inet_diag_sockid.idiag_sport
	IdiagDport    [2]byte   // __be16 inet_diag_sockid.idiag_dport
	IdiagSrc      [16]byte  // __be32[4] inet_diag_sockid.idiag_src
	IdiagDst      [16]byte  // __be32[4] inet_diag_sockid.idiag_dst
	IdiagIf       uint32    // __u32 inet_diag_sockid.idiag_if
	IdiagCookie   [2]uint32 // __u32[2] inet_diag_sockid.idiag_cookie
}

// InetDiagMsg is the struct inet_diag_msg from uapi/linux/inet_diag.h
type InetDiagMsg struct {
	IdiagFamily  uint8     // __u8 idiag_family
	IdiagState   uint8     // __u8 idiag_state
	IdiagTimer   uint8     // __u8 idiag_timer
	IdiagRetrans uint8     // __u8 idiag_retrans
	IdiagSport   [2]byte   // __be16 inet_diag_sockid.idiag_sport
	IdiagDport   [2]byte   // __be16 inet_diag_sockid.idiag_dport
	IdiagSrc     [16]byte  // __be32[4] inet_diag_sockid.idiag_src
	IdiagDst     [16]byte  // __be32[4] inet_diag_sockid.idiag_dst
	IdiagIf      uint32    // __u32 inet_diag_sockid.idiag_if
	IdiagCookie  [2]uint32 // __u32[2] inet_diag_sockid.idiag_cookie
	IdiagExpires uint32    // __u32 idiag_expires
	IdiagRqueue  uint32    // __u32 idiag_rqueue
	IdiagWqueue  uint32    // __u32 idiag_wqueue
	IdiagUid     uint32    // __u32 idiag_uid
	IdiagIndoe   uint32    // __u32 idiag_inode
}

// This two enums are from net/tcp_states.h
const (
	_              = iota
	TcpEstablished = iota // == 1
	TcpSynSent
	TcpSynRecv
	TcpFinWait1
	TcpFinWait2
	TcpTimeWait
	TcpClose
	TcpCloseWait
	TcpLastAck
	TcpListen
	TcpClosing
	TcpNewSynRecv
)

const TcpfEstablished = 1 << TcpEstablished
const TcpfSynSent = 1 << TcpSynSent
const TcpfSynRecv = 1 << TcpSynRecv
const TcpfFinWait1 = 1 << TcpFinWait1
const TcpfFinWait2 = 1 << TcpFinWait2
const TcpfTimeWait = 1 << TcpTimeWait
const TcpfClose = 1 << TcpClose
const TcpfCloseWait = 1 << TcpCloseWait
const TcpfLastAck = 1 << TcpLastAck
const TcpfListen = 1 << TcpListen
const TcpfClosing = 1 << TcpClosing
const TcpfNewSynRecv = 1 << TcpNewSynRecv

// Kernel headers import end here.

func isActiveTcpState(state uint8) bool {
	// Don't include TcpSynRecv - the server process isn't tied up until we send a SYN/ACK in response
	// and start actually processing the request

	// Almost all active server processes will be Established state
	return state == TcpEstablished ||
		// If we close first, the server process is tied up in these states until it sends the final
		// FIN/ACK. But not in TIME_WAIT.
		state == TcpFinWait1 ||
		state == TcpFinWait2 ||
		state == TcpClosing ||
		// If the client closes first, the server process is tied up until all the data exchange finishes.
		state == TcpCloseWait ||
		state == TcpLastAck
}

const ActiveTcpStatesBitmask = TcpfEstablished | TcpfFinWait1 | TcpfFinWait2 | TcpfClosing | TcpfCloseWait | TcpfLastAck

type NetlinkSocketStats struct {
	QueueSize     int
	ActiveWorkers int
}

type RaingutterNetlinkConnection struct {
	conn *netlink.Conn
}

func NewRaingutterNetlinkConnection() (*RaingutterNetlinkConnection, error) {
	conn, err := netlink.Dial(NetlinkSockDiag, nil)
	if err != nil {
		return nil, fmt.Errorf("dial NETLINK_SOCK_DIAG: %w", err)
	}

	return &RaingutterNetlinkConnection{
		conn: conn,
	}, nil
}

func (c *RaingutterNetlinkConnection) Close() {
	_ = c.conn.Close()
}

func (c *RaingutterNetlinkConnection) ReadStats(listenerPort uint16) (SocketStats, error) {
	var combinedStats SocketStats

	ip4Stats, err := c.readStatsForFamily(unix.AF_INET, listenerPort)
	if err != nil {
		return combinedStats, err
	}
	ip6Stats, err := c.readStatsForFamily(unix.AF_INET6, listenerPort)
	if err != nil {
		return combinedStats, err
	}

	if ip4Stats.ListenerInode != 0 {
		combinedStats.ListenerInode = ip4Stats.ListenerInode
	} else {
		combinedStats.ListenerInode = ip6Stats.ListenerInode
	}
	combinedStats.QueueSize = ip4Stats.QueueSize + ip6Stats.QueueSize
	combinedStats.ActiveWorkers = ip4Stats.ActiveWorkers + ip6Stats.ActiveWorkers
	return combinedStats, nil
}

func (c *RaingutterNetlinkConnection) readStatsForFamily(family int, listenerPort uint16) (SocketStats, error) {
	var statsRet SocketStats

	var req netlink.Message
	req.Header.Flags = netlink.Request | netlink.Dump
	req.Header.Type = SockDiagByFamily

	var diagReqStruct InetDiagReqV2
	diagReqStruct.SdiagFamily = uint8(family)
	diagReqStruct.SdiagProtocol = unix.IPPROTO_TCP
	diagReqStruct.IdaigStates = ActiveTcpStatesBitmask | TcpfListen
	// The field is named "source port" but to be clear, that means "local port" on
	// this machine. So filtering for IdiagSport == listenerPort in our netlink request
	// will mean we only get sockets that are caused by our server processes receiving
	// or listening for a connection, not any potentially outgoing requests they might make
	// on the same (remote) port number.
	binary.BigEndian.PutUint16(diagReqStruct.IdiagSport[:], listenerPort)

	req.Data = unsafe.Slice((*byte)(unsafe.Pointer(&diagReqStruct)), unsafe.Sizeof(diagReqStruct))

	replies, err := c.conn.Execute(req)
	if err != nil {
		return statsRet, fmt.Errorf("netlink error: %w", err)
	}
	for _, reply := range replies {
		diagReplyStruct := (*InetDiagMsg)(unsafe.Pointer(&reply.Data[0]))

		// Apparently means is in the process of being handed off to the kernel, according
		// to the raindrops code.
		if diagReplyStruct.IdiagIndoe == 0 {
			continue
		}

		if diagReplyStruct.IdiagState == TcpListen {
			// This is the listener socket.
			// n.b. - inodes are usually 64-bits on Linux (hence we have statsRet.ListenerInode
			// as uint64), but _socket_ inodes are only 32 bits (that's how they come out of the
			// netlink API). What happens if you have more than four billion sockets open in a
			// single network namespace is.... ???
			statsRet.ListenerInode = uint64(diagReplyStruct.IdiagIndoe)
			statsRet.QueueSize = uint64(diagReplyStruct.IdiagRqueue)
		} else if isActiveTcpState(diagReplyStruct.IdiagState) {
			statsRet.ActiveWorkers++
		}
	}
	return statsRet, nil
}
