//go:build linux

package egress

import (
	"encoding/binary"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The IP_TRANSPARENT egress sockets are opened privileged in the netns by
// fence.openTransparent (while the fence stage still holds the in-userns CAP_NET_ADMIN)
// and passed to the confined agent as inherited fds; the proxy serves them via
// ServeInherited. The helpers below recover per-datagram original destinations and send
// spoofed replies on those already-transparent fds.

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

// writeSpoofedUDP sends payload to `to` on the ALREADY-TRANSPARENT inherited listen
// socket uc, spoofing the source as `from` via an IP_PKTINFO control message. The
// confined agent has no CAP_NET_ADMIN to open its own IP_TRANSPARENT reply socket,
// but uc was opened privileged by Setup and carries IP_TRANSPARENT, so a per-datagram
// IPI_SPEC_DST lets a relayed reply appear to come straight from the real server
// (flow doc §6.9 reply path). WriteMsgUDP is safe to call concurrently with the read
// loop on the same UDPConn.
func writeSpoofedUDP(uc *net.UDPConn, from, to *net.UDPAddr, payload []byte) error {
	src := from.IP.To4()
	if src == nil {
		return fmt.Errorf("egress: spoof source %v is not IPv4", from.IP)
	}
	sz := int(unsafe.Sizeof(unix.Inet4Pktinfo{}))
	oob := make([]byte, unix.CmsgSpace(sz))
	h := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
	h.Level = unix.IPPROTO_IP
	h.Type = unix.IP_PKTINFO
	h.SetLen(unix.CmsgLen(sz))
	pi := (*unix.Inet4Pktinfo)(unsafe.Pointer(&oob[unix.CmsgLen(0)]))
	copy(pi.Spec_dst[:], src)
	_, _, err := uc.WriteMsgUDP(payload, oob, to)
	return err
}
