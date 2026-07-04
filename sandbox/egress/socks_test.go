package egress

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
)

// SOCKS5Connect drives the CONNECT handshake correctly: greeting, then a CONNECT
// request carrying the original destination, and it succeeds on REP=0x00.
func TestSOCKS5Connect(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	dst := &net.TCPAddr{IP: net.IPv4(93, 184, 216, 34), Port: 443}
	errc := make(chan error, 1)
	go func() { errc <- SOCKS5Connect(client, dst) }()

	// Server: read greeting, offer no-auth.
	greet := make([]byte, 3)
	if _, err := io.ReadFull(server, greet); err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	if greet[0] != 0x05 || greet[2] != 0x00 {
		t.Fatalf("bad greeting %v", greet)
	}
	if _, err := server.Write([]byte{0x05, 0x00}); err != nil {
		t.Fatalf("write method: %v", err)
	}

	// Read CONNECT request (VER CMD RSV ATYP=v4 + 4 addr + 2 port).
	req := make([]byte, 10)
	if _, err := io.ReadFull(server, req); err != nil {
		t.Fatalf("read request: %v", err)
	}
	if req[0] != 0x05 || req[1] != 0x01 || req[3] != 0x01 {
		t.Fatalf("bad request head %v", req[:4])
	}
	if !net.IP(req[4:8]).Equal(dst.IP.To4()) || binary.BigEndian.Uint16(req[8:10]) != 443 {
		t.Fatalf("request target mismatch: %v", req[4:])
	}
	// Reply success.
	if _, err := server.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatalf("write reply: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("SOCKS5Connect: %v", err)
	}
}

// A relay refusal (REP != 0) is surfaced as an error.
func TestSOCKS5ConnectRefused(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		io.ReadFull(server, make([]byte, 3))
		server.Write([]byte{0x05, 0x00})
		io.ReadFull(server, make([]byte, 10))
		server.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // REP=5 refused
	}()
	if err := SOCKS5Connect(client, &net.TCPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 80}); err == nil {
		t.Fatal("refusal must be an error")
	}
}
