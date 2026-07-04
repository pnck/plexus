package egress

import (
	"encoding/binary"
	"fmt"
	"net"
)

// The UDP egress tunnel to the control plane carries each intercepted datagram
// wrapped with its original destination, so the relay knows where to send it and a
// reply knows where it came from (flow doc §6.9). The tunnel is a single ordered
// stream, so frames are length-delimited. Both ends are our code, so the framing is
// ours (simpler than SOCKS UDP ASSOCIATE).
//
// Wire form:  [dstLen:1][dst:dstLen][payloadLen:2 BE][payload]
// dst is "ip:port" text — v4/v6 agnostic, no separate ATYP.

// EncodeDatagram frames one UDP datagram bound for dst.
func EncodeDatagram(dst *net.UDPAddr, payload []byte) ([]byte, error) {
	ds := dst.String()
	if len(ds) > 0xff {
		return nil, fmt.Errorf("egress: udp dst %q too long", ds)
	}
	if len(payload) > 0xffff {
		return nil, fmt.Errorf("egress: udp payload too large (%d bytes)", len(payload))
	}
	out := make([]byte, 0, 1+len(ds)+2+len(payload))
	out = append(out, byte(len(ds)))
	out = append(out, ds...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(payload)))
	return append(out, payload...), nil
}

// DecodeDatagram parses one frame from the front of buf, returning the destination
// text, its payload, and the total bytes consumed. ok is false (with no error) when
// buf holds only a partial frame, so a stream reader can wait for more bytes.
func DecodeDatagram(buf []byte) (dst string, payload []byte, n int, ok bool) {
	if len(buf) < 1 {
		return "", nil, 0, false
	}
	dl := int(buf[0])
	if len(buf) < 1+dl+2 {
		return "", nil, 0, false
	}
	plen := int(binary.BigEndian.Uint16(buf[1+dl : 1+dl+2]))
	end := 1 + dl + 2 + plen
	if len(buf) < end {
		return "", nil, 0, false
	}
	return string(buf[1 : 1+dl]), buf[1+dl+2 : end], end, true
}
