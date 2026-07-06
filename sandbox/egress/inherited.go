package egress

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"plexus/sandbox/netpol"
)

// Environment contract between Phase-0 Setup and the confined agent: Setup (which
// has CAP_NET_ADMIN) opens the IP_TRANSPARENT egress sockets in the netns and hands
// them down as inherited fds, plus the relay address + per-protocol policy, so the
// unprivileged agent can serve the proxy without any capability of its own.
const (
	EnvTCPFD  = "PLEXUS_EGRESS_TCP_FD" // inherited IP_TRANSPARENT TCP listener fd
	EnvUDPFD  = "PLEXUS_EGRESS_UDP_FD" // inherited IP_TRANSPARENT UDP socket fd
	EnvRelay  = "PLEXUS_EGRESS_RELAY"  // CP EgressRelay address, host:port
	EnvNetTCP = "PLEXUS_NET_TCP"       // redirect|reject|drop
	EnvNetUDP = "PLEXUS_NET_UDP"
)

// ServeInherited starts the egress proxy on the transparent sockets Setup created
// and passed down as inherited fds — the confined agent needs no capability to
// serve them. It reads the fd numbers + relay + policy from the environment. When
// PLEXUS_EGRESS_TCP_FD is absent (chat / dev, no netns fence) it is a no-op.
func ServeInherited() (stop func(), err error) {
	if os.Getenv(EnvTCPFD) == "" {
		return func() {}, nil
	}
	p := &Proxy{
		Policy: netpol.NetPolicy{TCP: netpol.ParseAction(os.Getenv(EnvNetTCP)), UDP: netpol.ParseAction(os.Getenv(EnvNetUDP))},
		Relay:  os.Getenv(EnvRelay),
	}
	tcpLn, err := inheritedListener(EnvTCPFD, "egress-tcp")
	if err != nil {
		return nil, err
	}
	udpConn, err := inheritedPacket(EnvUDPFD, "egress-udp")
	if err != nil {
		_ = tcpLn.Close()
		return nil, err
	}
	go func() { _ = p.ServeTCP(tcpLn) }()
	go func() { _ = p.ServeUDP(udpConn) }()
	return func() { _ = tcpLn.Close(); _ = udpConn.Close() }, nil
}

func inheritedListener(env, name string) (net.Listener, error) {
	fd, err := fdFromEnv(env)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close() // FileListener dups; the original fd can be released
	return net.FileListener(f)
}

func inheritedPacket(env, name string) (*net.UDPConn, error) {
	fd, err := fdFromEnv(env)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	pc, err := net.FilePacketConn(f)
	if err != nil {
		return nil, err
	}
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("egress: %s is not a UDP socket", env)
	}
	return uc, nil
}

func fdFromEnv(env string) (int, error) {
	fd, err := strconv.Atoi(os.Getenv(env))
	if err != nil || fd < 3 {
		return 0, fmt.Errorf("egress: bad %s=%q", env, os.Getenv(env))
	}
	return fd, nil
}
