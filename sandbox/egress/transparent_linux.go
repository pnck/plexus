//go:build linux

package egress

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// transparentTCP sets IP_TRANSPARENT on a listening socket.
func transparentTCP(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_TRANSPARENT, 1)
	}); err != nil {
		return err
	}
	return serr
}

// ListenTransparentTCP opens a TCP listener with IP_TRANSPARENT: the kernel delivers
// TPROXY-intercepted connections here with the ORIGINAL destination as the accepted
// socket's local address (flow doc §6.9), so no per-connection syscall is needed to
// recover it. addr is the local egress port the nft rules mark-and-reroute to.
func ListenTransparentTCP(addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: transparentTCP}
	return lc.Listen(context.Background(), "tcp", addr)
}

// ListenTransparentUDP opens a UDP socket with IP_TRANSPARENT + IP_RECVORIGDSTADDR,
// so recvmsg reveals each intercepted datagram's original destination.
func ListenTransparentUDP(addr string) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			if e := unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_TRANSPARENT, 1); e != nil {
				serr = e
				return
			}
			serr = unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_RECVORIGDSTADDR, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp", addr)
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

// readUDPOrigDst reads one intercepted datagram, returning its length, sender, and
// the ORIGINAL destination (from the IP_RECVORIGDSTADDR control message).
func readUDPOrigDst(c *net.UDPConn, buf, oob []byte) (n int, src, origDst *net.UDPAddr, err error) {
	n, oobn, _, src, err := c.ReadMsgUDP(buf, oob)
	if err != nil {
		return 0, nil, nil, err
	}
	origDst, err = parseOrigDst(oob[:oobn])
	return n, src, origDst, err
}

func parseOrigDst(oob []byte) (*net.UDPAddr, error) {
	cmsgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}
	for _, m := range cmsgs {
		if m.Header.Level == unix.SOL_IP && m.Header.Type == unix.IP_RECVORIGDSTADDR {
			if len(m.Data) < unix.SizeofSockaddrInet4 {
				return nil, fmt.Errorf("egress: short origdst cmsg")
			}
			sa := (*unix.RawSockaddrInet4)(unsafe.Pointer(&m.Data[0]))
			// Port is network byte order in memory; read it order-independently.
			port := binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&sa.Port))[:])
			return &net.UDPAddr{IP: net.IP(sa.Addr[:]).To4(), Port: int(port)}, nil
		}
	}
	return nil, fmt.Errorf("egress: no IP_RECVORIGDSTADDR control message")
}

// spoofedUDPSocket opens a UDP socket bound (via IP_TRANSPARENT) to a non-local
// address `from`, so a relayed reply written from it appears to come straight from
// the real server the agent addressed (flow doc §6.9 reply path).
func spoofedUDPSocket(from *net.UDPAddr) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			if e := unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_TRANSPARENT, 1); e != nil {
				serr = e
				return
			}
			// concurrent reply sockets may bind the same spoofed source (two flows
			// talking to the same server) — allow it.
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp", from.String())
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}
