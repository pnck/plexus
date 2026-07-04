package egress

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"plexus/sandbox/caps"
	"plexus/sandbox/netpol"
)

// RequiredCaps reports that the transparent listeners need CAP_NET_ADMIN
// (IP_TRANSPARENT), making Proxy a caps.Requirer so the launcher counts it in the
// central, up-front capability check. The transparent sockets are created in the
// privileged Phase 0 and handed to the confined agent, which then needs no cap to
// serve them.
func (p *Proxy) RequiredCaps() caps.Set { return caps.Of(caps.NetAdmin) }

// Proxy is the agent-side transparent egress proxy that runs inside the per-agent
// netns. nft TPROXY-intercepts every allowed outbound flow (TCP/UDP) to this proxy's
// local port; the proxy attributes the flow to its process, judges it against the
// NetPolicy, and relays the allowed ones to the control-plane EgressRelay, which
// dials out with the CP's own network (flow doc §6). brain and every child process
// are treated identically and none can bypass it (§6.6/§6.7).
type Proxy struct {
	Policy netpol.NetPolicy // per-protocol disposition (redirect / reject / drop)
	Relay  string           // CP EgressRelay address, host:port

	// DialRelay opens a connection to the CP relay; overridable in tests. Defaults to
	// a plain TCP dial to Relay.
	DialRelay func() (net.Conn, error)
}

func (p *Proxy) dialRelay() (net.Conn, error) {
	if p.DialRelay != nil {
		return p.DialRelay()
	}
	return net.Dial("tcp", p.Relay)
}

// ServeTCP accepts TPROXY-intercepted TCP connections from ln (opened with
// ListenTransparentTCP) and relays each. It returns when ln is closed.
func (p *Proxy) ServeTCP(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go func() {
			if err := p.handleTCP(c); err != nil {
				slog.Debug("egress tcp", "err", err)
			}
		}()
	}
}

// handleTCP relays one intercepted TCP connection to its original destination. Under
// TPROXY the original destination IS the accepted socket's local address.
func (p *Proxy) handleTCP(client net.Conn) error {
	defer client.Close()
	orig, ok := client.LocalAddr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("no original destination on %v", client.LocalAddr())
	}

	switch p.Policy.Decide(netpol.TCP) {
	case netpol.Redirect:
		// allowed — relay via the CP
	case netpol.Reject:
		return fmt.Errorf("tcp to %s rejected by policy", orig)
	default: // Drop — silently close
		return nil
	}

	if src, ok := client.RemoteAddr().(*net.TCPAddr); ok {
		if a, err := Attribute("tcp", src.IP, src.Port); err == nil && a.PID != 0 {
			slog.Debug("egress tcp", "pid", a.PID, "comm", a.Comm, "dst", orig.String())
		}
	}

	up, err := p.dialRelay()
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	defer up.Close()
	if err := SOCKS5Connect(up, orig); err != nil {
		return err
	}
	pipe(client, up)
	return nil
}

// pipe copies bidirectionally until either side closes, then tears down both.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		_ = dst.Close()
		_ = src.Close()
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// ServeUDP receives TPROXY-intercepted datagrams on uc (from ListenTransparentUDP),
// relays allowed ones to the CP over a per-source tunnel, and spoofs replies back so
// they appear to come from the real destination. It returns when uc is closed.
func (p *Proxy) ServeUDP(uc *net.UDPConn) error {
	flows := &udpFlows{proxy: p, bysrc: map[string]*udpFlow{}}
	defer flows.closeAll()

	buf := make([]byte, 64*1024)
	oob := make([]byte, 1024)
	for {
		n, src, dst, err := readUDPOrigDst(uc, buf, oob)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		// UDP has no clean refusal; reject and drop both just don't forward.
		if p.Policy.Decide(netpol.UDP) != netpol.Redirect {
			continue
		}
		if a, err := Attribute("udp", src.IP, src.Port); err == nil && a.PID != 0 {
			slog.Debug("egress udp", "pid", a.PID, "comm", a.Comm, "dst", dst.String())
		}
		flows.forward(src, dst, append([]byte(nil), buf[:n]...))
	}
}

// udpFlow is one agent source's tunnel to the CP relay, plus the goroutine reading
// relayed replies and spoofing them back toward that source.
type udpFlow struct {
	tunnel net.Conn
	src    *net.UDPAddr
}

type udpFlows struct {
	proxy *Proxy
	mu    sync.Mutex
	bysrc map[string]*udpFlow
}

func (f *udpFlows) forward(src, dst *net.UDPAddr, payload []byte) {
	fl, err := f.get(src)
	if err != nil {
		slog.Debug("egress udp", "err", err)
		return
	}
	frame, err := EncodeDatagram(dst, payload)
	if err != nil {
		slog.Debug("egress udp encode", "err", err)
		return
	}
	if _, err := fl.tunnel.Write(frame); err != nil {
		f.drop(src)
	}
}

func (f *udpFlows) get(src *net.UDPAddr) (*udpFlow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fl, ok := f.bysrc[src.String()]; ok {
		return fl, nil
	}
	conn, err := f.proxy.dialRelay()
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}
	fl := &udpFlow{tunnel: conn, src: src}
	f.bysrc[src.String()] = fl
	go f.replies(fl)
	return fl, nil
}

// replies reads relayed datagrams from the tunnel and writes each back to the
// agent source, spoofing the source address as the datagram's original destination.
func (f *udpFlows) replies(fl *udpFlow) {
	defer f.drop(fl.src)
	acc := make([]byte, 0, 64*1024)
	rd := make([]byte, 32*1024)
	for {
		n, err := fl.tunnel.Read(rd)
		if n > 0 {
			acc = append(acc, rd[:n]...)
			for {
				dst, payload, used, ok := DecodeDatagram(acc)
				if !ok {
					break
				}
				if from, e := net.ResolveUDPAddr("udp", dst); e == nil {
					f.spoofReply(from, fl.src, payload)
				}
				acc = acc[used:]
			}
		}
		if err != nil {
			return
		}
	}
}

func (f *udpFlows) spoofReply(from, to *net.UDPAddr, payload []byte) {
	s, err := spoofedUDPSocket(from)
	if err != nil {
		slog.Debug("egress udp reply", "err", err)
		return
	}
	defer s.Close()
	_, _ = s.WriteToUDP(payload, to)
}

func (f *udpFlows) drop(src *net.UDPAddr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fl, ok := f.bysrc[src.String()]; ok {
		_ = fl.tunnel.Close()
		delete(f.bysrc, src.String())
	}
}

func (f *udpFlows) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, fl := range f.bysrc {
		_ = fl.tunnel.Close()
		delete(f.bysrc, k)
	}
}
