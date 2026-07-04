package egress

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// SOCKS5Connect performs a SOCKS5 CONNECT handshake over rw to reach dst — the
// flow's original destination — returning once the control-plane EgressRelay has
// established the upstream connection (flow doc §6.5 step 3). No client auth: the
// relay authenticates the agent by its source connection, not credentials
// (§6.4). This is the agent-side proxy asking the CP to dial out on its behalf.
func SOCKS5Connect(rw io.ReadWriter, dst *net.TCPAddr) error {
	// Greeting: VER=5, one method, 0x00 = no auth.
	if _, err := rw.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return fmt.Errorf("socks: greeting: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(rw, reply); err != nil {
		return fmt.Errorf("socks: method reply: %w", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		return fmt.Errorf("socks: no-auth not accepted (ver=%d method=%d)", reply[0], reply[1])
	}

	// Request: VER=5 CMD=CONNECT RSV=0 ATYP addr port.
	req := []byte{0x05, 0x01, 0x00}
	if v4 := dst.IP.To4(); v4 != nil {
		req = append(req, 0x01)
		req = append(req, v4...)
	} else {
		req = append(req, 0x04)
		req = append(req, dst.IP.To16()...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(dst.Port))
	if _, err := rw.Write(req); err != nil {
		return fmt.Errorf("socks: connect request: %w", err)
	}

	// Reply: VER REP RSV ATYP BND.ADDR BND.PORT. REP 0x00 = succeeded.
	head := make([]byte, 4)
	if _, err := io.ReadFull(rw, head); err != nil {
		return fmt.Errorf("socks: connect reply: %w", err)
	}
	if head[0] != 0x05 {
		return fmt.Errorf("socks: bad reply version %d", head[0])
	}
	if head[1] != 0x00 {
		return fmt.Errorf("socks: relay refused (rep=%d)", head[1])
	}
	var bnd int
	switch head[3] {
	case 0x01:
		bnd = 4
	case 0x04:
		bnd = 16
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(rw, l); err != nil {
			return fmt.Errorf("socks: reply domain len: %w", err)
		}
		bnd = int(l[0])
	default:
		return fmt.Errorf("socks: unknown reply atyp %d", head[3])
	}
	if _, err := io.CopyN(io.Discard, rw, int64(bnd+2)); err != nil { // drain BND.ADDR+PORT
		return fmt.Errorf("socks: drain bound addr: %w", err)
	}
	return nil
}
