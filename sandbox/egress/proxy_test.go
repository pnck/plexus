package egress

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"plexus/sandbox/netpol"
)

// stubRelay is a minimal SOCKS5 server standing in for the CP EgressRelay: it completes
// the no-auth CONNECT handshake (ignoring the requested target) and then echoes the
// tunnelled payload, so a redirected flow round-trips through the real proxy relay loop.
func stubRelay(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("stub relay listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveStubRelay(c)
		}
	}()
	return ln
}

func serveStubRelay(c net.Conn) {
	defer c.Close()
	// greeting: VER, NMETHODS, METHODS...
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return
	}
	if _, err := io.CopyN(io.Discard, c, int64(hdr[1])); err != nil {
		return
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil { // no-auth
		return
	}
	// request: VER CMD RSV ATYP addr port
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	var alen int
	switch req[3] {
	case 0x01:
		alen = 4
	case 0x04:
		alen = 16
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return
		}
		alen = int(l[0])
	default:
		return
	}
	if _, err := io.CopyN(io.Discard, c, int64(alen+2)); err != nil { // addr + port
		return
	}
	// success reply: VER REP RSV ATYP BND.ADDR(4) BND.PORT(2)
	if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	_, _ = io.Copy(c, c) // echo the tunnelled bytes back
}

// A redirected TCP flow must run the full proxy relay loop: judge -> dial relay ->
// SOCKS5 CONNECT for the original destination -> bidirectional pipe. (The kernel side —
// TPROXY interception, IP_TRANSPARENT original-dst recovery, and fd inheritance through
// the bwrap exec — is the E4.6.7 integration that lands with the E5 EgressRelay; here the
// accepted socket's local address stands in for the original destination.)
func TestProxyServeTCPRelaysRedirectedFlow(t *testing.T) {
	relay := stubRelay(t)
	defer relay.Close()

	var dials int32
	p := &Proxy{
		Policy: netpol.NetPolicy{TCP: netpol.Redirect},
		DialRelay: func() (net.Conn, error) {
			atomic.AddInt32(&dials, 1)
			return net.Dial("tcp", relay.Addr().String())
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("egress listen: %v", err)
	}
	defer ln.Close()
	go func() { _ = p.ServeTCP(ln) }()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer c.Close()

	msg := []byte("ping-through-the-relay")
	if _, err := c.Write(msg); err != nil {
		t.Fatalf("client write: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read echo through relay: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("relayed echo mismatch: got %q want %q", got, msg)
	}
	if n := atomic.LoadInt32(&dials); n != 1 {
		t.Fatalf("expected exactly one relay dial, got %d", n)
	}
}

// reject and drop must NOT relay: the proxy closes the intercepted connection without ever
// dialing the CP relay.
func TestProxyRejectAndDropDoNotRelay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action netpol.NetAction
	}{
		{"reject", netpol.Reject},
		{"drop", netpol.Drop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dials int32
			p := &Proxy{
				Policy: netpol.NetPolicy{TCP: tc.action},
				DialRelay: func() (net.Conn, error) {
					atomic.AddInt32(&dials, 1)
					return nil, fmt.Errorf("relay must not be dialed for %s", tc.name)
				},
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer ln.Close()
			go func() { _ = p.ServeTCP(ln) }()

			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer c.Close()

			// The proxy closes the client without relaying, so a read hits EOF.
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := c.Read(make([]byte, 1)); err == nil {
				t.Fatalf("%s: expected the proxy to close the connection", tc.name)
			}
			if n := atomic.LoadInt32(&dials); n != 0 {
				t.Fatalf("%s: relay must not be dialed, got %d", tc.name, n)
			}
		})
	}
}
