package egress

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"plexus/sandbox/caps"
	"plexus/sandbox/netpol"
)

const (
	relayDialTimeout = 10 * time.Second // dialing the CP relay
	udpIdleTimeout   = 60 * time.Second // evict a UDP flow after this long with no reply traffic
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
	d := net.Dialer{Timeout: relayDialTimeout}
	return d.Dial("tcp", p.Relay)
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

	// Per-process attribution scans /proc (O(system fds)); only pay for it when this
	// protocol is audited.
	if p.Policy.Logs(netpol.TCP) {
		if src, ok := client.RemoteAddr().(*net.TCPAddr); ok {
			if a, err := Attribute("tcp", src.IP, src.Port); err == nil && a.PID != 0 {
				slog.Debug("egress tcp", "pid", a.PID, "comm", a.Comm, "dst", orig.String())
			}
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
	// Keepalive reaps a genuinely-dead half-open flow (peer gone after shutdown(WR))
	// without cutting a live-but-idle stream (a slow LLM reply keeps answering probes).
	setKeepAlive(client)
	setKeepAlive(up)
	pipe(client, up)
	return nil
}

func setKeepAlive(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
	}
}

// pipe copies bidirectionally. When one direction reaches EOF it half-closes the
// write side of the peer (so a client's shutdown(SHUT_WR) does not truncate the
// reply direction); the caller closes both conns fully afterward.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
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
	flows := &udpFlows{proxy: p, uc: uc, bysrc: map[string]*udpFlow{}}
	defer flows.closeAll()

	buf := make([]byte, 64*1024)
	oob := make([]byte, 1024)
	for {
		n, src, dst, err := readUDPOrigDst(uc, buf, oob)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// A single datagram we can't parse (e.g. a missing orig-dst cmsg) must not
			// kill the whole proxy — skip it and keep serving.
			slog.Debug("egress udp recv", "err", err)
			continue
		}
		// UDP has no clean refusal; reject and drop both just don't forward.
		if p.Policy.Decide(netpol.UDP) != netpol.Redirect {
			continue
		}
		if p.Policy.Logs(netpol.UDP) {
			if a, err := Attribute("udp", src.IP, src.Port); err == nil && a.PID != 0 {
				slog.Debug("egress udp", "pid", a.PID, "comm", a.Comm, "dst", dst.String())
			}
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
	uc    *net.UDPConn // the inherited transparent listen socket, reused to spoof replies
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
		f.drop(fl)
	}
}

// get returns the flow for src, creating one if absent. It dials the relay OUTSIDE
// the lock (never blocking the whole flow map on a slow relay) and re-checks after.
func (f *udpFlows) get(src *net.UDPAddr) (*udpFlow, error) {
	key := src.String()
	f.mu.Lock()
	if fl, ok := f.bysrc[key]; ok {
		f.mu.Unlock()
		return fl, nil
	}
	f.mu.Unlock()

	conn, err := f.proxy.dialRelay()
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}

	f.mu.Lock()
	if fl, ok := f.bysrc[key]; ok { // another datagram raced us to it
		f.mu.Unlock()
		_ = conn.Close()
		return fl, nil
	}
	fl := &udpFlow{tunnel: conn, src: src}
	f.bysrc[key] = fl
	f.mu.Unlock()
	go f.replies(fl)
	return fl, nil
}

// replies reads relayed datagrams from the tunnel and spoofs each back to the agent
// source. An idle read deadline evicts the flow so ephemeral-port clients (e.g. DNS,
// a fresh source port per query) don't leak a tunnel + goroutine each.
func (f *udpFlows) replies(fl *udpFlow) {
	defer f.drop(fl)
	acc := make([]byte, 0, 64*1024)
	rd := make([]byte, 32*1024)
	for {
		_ = fl.tunnel.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		n, err := fl.tunnel.Read(rd)
		if n > 0 {
			acc = append(acc, rd[:n]...)
			for {
				dst, payload, used, ok := DecodeDatagram(acc)
				if !ok {
					break
				}
				if from, e := net.ResolveUDPAddr("udp", dst); e == nil {
					if werr := writeSpoofedUDP(f.uc, from, fl.src, payload); werr != nil {
						slog.Debug("egress udp reply", "err", werr)
					}
				}
				acc = acc[used:]
			}
		}
		if err != nil {
			return // EOF, idle timeout, or tunnel error -> evict
		}
	}
}

// drop tears down fl only if it is STILL the registered flow for its source — an
// identity compare-and-delete that stops a stale goroutine from evicting a freshly
// re-created flow for the same source (an ABA race).
func (f *udpFlows) drop(fl *udpFlow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.bysrc[fl.src.String()]; ok && cur == fl {
		_ = fl.tunnel.Close()
		delete(f.bysrc, fl.src.String())
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
