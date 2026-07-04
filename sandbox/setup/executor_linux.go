//go:build linux

package setup

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"plexus/sandbox/cgroup"
	"plexus/sandbox/netpol"
)

// OSExecutor is the real privileged Phase-0 Executor (E4.6.4.2). It drives the
// kernel directly through netlink — vishvananda/netlink for the netns/veth/route,
// google/nftables for the egress fence — and writes cgroup-v2 files, with NO ip /
// nft / tc binaries, keeping plexus a single self-contained executable. It requires
// CAP_NET_ADMIN and a writable cgroup subtree (Phase 0 runs as the privileged Setup,
// flow doc §7). Runtime verification belongs in a privileged env.
//
// The nft builder mirrors the golden netpol.GenerateNFT text rule-for-rule (deny-all
// policy, loopback/bus accept, ct established, ct-count cap, per-protocol redirect
// mark / reject / drop, and the `log` prefix) — the two representations must stay in
// sync by hand until a privileged golden-equivalence test locks them together.
type OSExecutor struct {
	ns map[string]netns.NsHandle // named netns handles, by name
}

// NewOSExecutor returns a ready OSExecutor.
func NewOSExecutor() *OSExecutor { return &OSExecutor{ns: map[string]netns.NsHandle{}} }

// CreateNetns creates a persistent named network namespace (bind-mounted at
// /var/run/netns/<name>) and keeps its handle for the later steps. The current
// thread is switched into it transiently and restored.
func (x *OSExecutor) CreateNetns(name string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	orig, err := netns.Get()
	if err != nil {
		return fmt.Errorf("get current netns: %w", err)
	}
	defer func() { _ = netns.Set(orig); _ = orig.Close() }()

	h, err := netns.NewNamed(name)
	if err != nil {
		return fmt.Errorf("create netns %s: %w", name, err)
	}
	x.ns[name] = h
	return nil
}

func (x *OSExecutor) CreateVethPair(host, peer string) error {
	la := netlink.NewLinkAttrs()
	la.Name = host
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: la, PeerName: peer}); err != nil {
		return fmt.Errorf("create veth %s<->%s: %w", host, peer, err)
	}
	return nil
}

func (x *OSExecutor) MoveToNetns(iface, name string) error {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return fmt.Errorf("find %s: %w", iface, err)
	}
	h, ok := x.ns[name]
	if !ok {
		return fmt.Errorf("unknown netns %s", name)
	}
	return netlink.LinkSetNsFd(link, int(h))
}

// handle returns a netlink handle bound to the named netns ("" = the host/current
// namespace). The caller must Delete() it.
func (x *OSExecutor) handle(name string) (*netlink.Handle, error) {
	if name == "" {
		return netlink.NewHandle()
	}
	h, ok := x.ns[name]
	if !ok {
		return nil, fmt.Errorf("unknown netns %s", name)
	}
	return netlink.NewHandleAt(h)
}

func (x *OSExecutor) SetAddr(name, iface, cidr string) error {
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return fmt.Errorf("parse addr %s: %w", cidr, err)
	}
	h, err := x.handle(name)
	if err != nil {
		return err
	}
	defer h.Delete()
	link, err := h.LinkByName(iface)
	if err != nil {
		return fmt.Errorf("find %s: %w", iface, err)
	}
	return h.AddrAdd(link, addr)
}

func (x *OSExecutor) SetLinkUp(name, iface string) error {
	h, err := x.handle(name)
	if err != nil {
		return err
	}
	defer h.Delete()
	link, err := h.LinkByName(iface)
	if err != nil {
		return fmt.Errorf("find %s: %w", iface, err)
	}
	return h.LinkSetUp(link)
}

func (x *OSExecutor) AddDefaultRoute(name, gateway string) error {
	gw := net.ParseIP(gateway)
	if gw == nil {
		return fmt.Errorf("bad gateway %q", gateway)
	}
	h, err := x.handle(name)
	if err != nil {
		return err
	}
	defer h.Delete()
	return h.RouteAdd(&netlink.Route{Gw: gw}) // Dst nil => default route
}

// ApplyFence installs the nft egress fence and, for a redirected protocol, the
// TPROXY reroute (fwmark rule + local route), both in the netns.
func (x *OSExecutor) ApplyFence(name string, policy netpol.NetPolicy, params netpol.Params) error {
	if err := x.applyNFT(name, policy, params); err != nil {
		return fmt.Errorf("nft: %w", err)
	}
	if policy.Decide(netpol.TCP) == netpol.Redirect || policy.Decide(netpol.UDP) == netpol.Redirect {
		if err := x.applyReroute(name, params); err != nil {
			return fmt.Errorf("tproxy reroute: %w", err)
		}
	}
	return nil
}

// applyReroute mirrors netpol.GenerateIPRules: `ip rule add fwmark M lookup T` +
// `ip route add local default dev lo table T`, so TPROXY-marked local output is
// redelivered to the transparent proxy.
func (x *OSExecutor) applyReroute(name string, params netpol.Params) error {
	h, err := x.handle(name)
	if err != nil {
		return err
	}
	defer h.Delete()

	rule := netlink.NewRule()
	rule.Mark = params.Mark
	rule.Table = params.Table
	if err := h.RuleAdd(rule); err != nil {
		return fmt.Errorf("ip rule: %w", err)
	}
	lo, err := h.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("find lo: %w", err)
	}
	_, defDst, _ := net.ParseCIDR("0.0.0.0/0")
	return h.RouteAdd(&netlink.Route{
		Type:      unix.RTN_LOCAL,
		Scope:     unix.RT_SCOPE_HOST, // matches `ip route add local …`
		Table:     params.Table,
		Dst:       defDst,
		LinkIndex: lo.Attrs().Index,
	})
}

// applyNFT builds the `table inet mesh` / `chain out` fence programmatically,
// mirroring netpol.GenerateNFT.
func (x *OSExecutor) applyNFT(name string, policy netpol.NetPolicy, params netpol.Params) error {
	var opts []nftables.ConnOption
	if name != "" {
		h, ok := x.ns[name]
		if !ok {
			return fmt.Errorf("unknown netns %s", name)
		}
		opts = append(opts, nftables.WithNetNSFd(int(h)))
	}
	c, err := nftables.New(opts...)
	if err != nil {
		return err
	}

	drop := nftables.ChainPolicyDrop
	table := c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: "mesh"})
	chain := c.AddChain(&nftables.Chain{
		Name:     "out",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &drop,
	})
	add := func(exprs []expr.Any) {
		if exprs != nil {
			c.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: exprs})
		}
	}

	// ip daddr 127.0.0.0/8 accept
	add(append(ipv4DaddrMasked(net.IPv4(127, 0, 0, 0).To4(), []byte{0xff, 0, 0, 0}), acceptVerdict()...))
	// ip daddr <CP> tcp dport <port> accept — the bus, and the relay (so the proxy's
	// own upstream to the CP relay isn't re-marked into a TPROXY loop).
	if cp := net.ParseIP(params.CP).To4(); cp != nil {
		cpTCPAccept := func(port int) {
			e := ipv4Daddr(cp)
			e = append(e, l4proto(unix.IPPROTO_TCP)...)
			e = append(e,
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: be16(port)},
			)
			add(append(e, acceptVerdict()...))
		}
		cpTCPAccept(params.BusPort)
		if params.RelayPort > 0 {
			cpTCPAccept(params.RelayPort)
		}
	}
	// ct state established,related accept
	add([]expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4,
			Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
			Xor:  binaryutil.NativeEndian.PutUint32(0)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
		&expr.Verdict{Kind: expr.VerdictAccept},
	})
	// ct state new meta l4proto {tcp,udp} ct count over N drop — one shared per-agent
	// concurrency cap across both protocols (an anonymous set gives a single counter).
	if params.MaxConns > 0 {
		set := &nftables.Set{Table: table, Anonymous: true, Constant: true, KeyType: nftables.TypeInetProto}
		if err := c.AddSet(set, []nftables.SetElement{
			{Key: []byte{unix.IPPROTO_TCP}},
			{Key: []byte{unix.IPPROTO_UDP}},
		}); err != nil {
			return err
		}
		add([]expr.Any{
			&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4,
				Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitNEW),
				Xor:  binaryutil.NativeEndian.PutUint32(0)},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Lookup{SourceRegister: 1, SetName: set.Name, SetID: set.ID},
			&expr.Connlimit{Count: uint32(params.MaxConns), Flags: 1}, // Flags 1 = over (invert)
			&expr.Verdict{Kind: expr.VerdictDrop},
		})
	}

	// per-protocol disposition (+ optional log)
	add(protoRule(unix.IPPROTO_TCP, policy.Decide(netpol.TCP), params.Mark, true, policy.Logs(netpol.TCP)))
	add(protoRule(unix.IPPROTO_UDP, policy.Decide(netpol.UDP), params.Mark, false, policy.Logs(netpol.UDP)))

	return c.Flush()
}

// protoRule mirrors netpol.protoRule: redirect -> set the TPROXY mark and accept;
// reject -> refuse (tcp reset / icmp); drop -> no rule unless logging (then log+drop),
// otherwise it falls through to the chain policy drop. A `log` prefixes the line.
func protoRule(proto uint8, action netpol.NetAction, mark uint32, tcp, log bool) []expr.Any {
	if action == netpol.Drop && !log {
		return nil // falls through to policy drop
	}
	e := l4proto(proto)
	if log {
		name := "udp"
		if tcp {
			name = "tcp"
		}
		e = append(e, &expr.Log{Key: 1 << unix.NFTA_LOG_PREFIX, Data: []byte("egress-" + name + " ")})
	}
	switch action {
	case netpol.Redirect:
		return append(e,
			&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(mark)},
			&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
			&expr.Verdict{Kind: expr.VerdictAccept},
		)
	case netpol.Reject:
		if tcp {
			return append(e, &expr.Reject{Type: unix.NFT_REJECT_TCP_RST})
		}
		// inet-family chain: use the family-agnostic ICMPX reject (NFT_REJECT_ICMP_UNREACH
		// is IPv4-only and needs an nfproto dependency this rule doesn't carry).
		return append(e, &expr.Reject{Type: unix.NFT_REJECT_ICMPX_UNREACH, Code: unix.NFT_REJECT_ICMPX_PORT_UNREACH})
	default: // Drop with logging -> log then drop
		return append(e, &expr.Verdict{Kind: expr.VerdictDrop})
	}
}

// ipv4Daddr matches `meta nfproto ipv4` + `ip daddr == addr` into register 1.
func ipv4Daddr(addr net.IP) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr.To4()},
	}
}

// ipv4DaddrMasked is ipv4Daddr with a network mask (for a CIDR match).
func ipv4DaddrMasked(net4 net.IP, mask []byte) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: net4.To4()},
	}
}

func l4proto(proto uint8) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
	}
}

func acceptVerdict() []expr.Any { return []expr.Any{&expr.Verdict{Kind: expr.VerdictAccept}} }

func be16(v int) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, uint16(v))
	return b
}

// CreateCgroup makes a cgroup-v2 group and writes its limits; a zero limit is left
// at the parent default.
func (x *OSExecutor) CreateCgroup(name string, lim CgroupLimits) error {
	// Reuse the E4.3 cgroup layer instead of a raw top-level mkdir: it creates the
	// group UNDER this process's own cgroup, sets memory/pids, joins this process (so
	// the exec'd agent inherits it), and — crucially — degrades to ErrUnavailable
	// when no delegated cgroup-v2 subtree is writable (e.g. an unprivileged
	// container), so Setup falls back to the rlimit floor rather than failing.
	// (CPUMax is not yet handled by the shared cgroup layer.)
	if _, err := cgroup.Apply(name, cgroup.Limits{MemoryMax: lim.MemoryMax, PidsMax: lim.PidsMax}); err != nil {
		if errors.Is(err, cgroup.ErrUnavailable) {
			slog.Warn("setup: cgroup delegation unavailable — relying on the rlimit floor", "agent", name)
			return nil
		}
		return err
	}
	return nil
}

// EnterAndExec joins the prepared netns + cgroup, opens the IP_TRANSPARENT egress
// sockets inside the netns (while still privileged) and hands them to the child as
// inherited fds, then execs the agent — replacing this process, so its parent stays
// the caller (the persistent CP). The OS thread is locked so the netns setns binds
// the thread that immediately execs.
func (x *OSExecutor) EnterAndExec(name, _ string, egressPort int, argv, env []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty argv")
	}
	runtime.LockOSThread()

	h, ok := x.ns[name]
	if !ok {
		return fmt.Errorf("unknown netns %s", name)
	}
	if err := unix.Setns(int(h), unix.CLONE_NEWNET); err != nil {
		return fmt.Errorf("setns %s: %w", name, err)
	}

	// Open the transparent egress sockets now (privileged, in the netns) and pass
	// them down: the confined agent has no CAP_NET_ADMIN to open them itself. The fds
	// are left inheritable (no CLOEXEC) so they survive the exec chain.
	if egressPort > 0 {
		tcpFD, err := openTransparent(unix.SOCK_STREAM, egressPort)
		if err != nil {
			return fmt.Errorf("egress tcp socket: %w", err)
		}
		udpFD, err := openTransparent(unix.SOCK_DGRAM, egressPort)
		if err != nil {
			return fmt.Errorf("egress udp socket: %w", err)
		}
		env = append(env,
			"PLEXUS_EGRESS_TCP_FD="+strconv.Itoa(tcpFD),
			"PLEXUS_EGRESS_UDP_FD="+strconv.Itoa(udpFD),
		)
	}

	// The cgroup was created and JOINED in CreateCgroup (same process, via
	// cgroup.Apply), so the exec'd agent already inherits it — nothing to do here.
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("lookpath %s: %w", argv[0], err)
	}
	return syscall.Exec(bin, argv, env)
}

// openTransparent creates a bound IP_TRANSPARENT socket on 127.0.0.1:port and
// returns its fd (left inheritable — no SOCK_CLOEXEC — for the child to serve). UDP
// sockets also get IP_RECVORIGDSTADDR so the proxy can recover original destinations.
func openTransparent(sotype, port int) (int, error) {
	fd, err := unix.Socket(unix.AF_INET, sotype, 0)
	if err != nil {
		return -1, err
	}
	fail := func(e error) (int, error) { _ = unix.Close(fd); return -1, e }
	if err := unix.SetsockoptInt(fd, unix.SOL_IP, unix.IP_TRANSPARENT, 1); err != nil {
		return fail(err)
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
		return fail(err)
	}
	if sotype == unix.SOCK_DGRAM {
		if err := unix.SetsockoptInt(fd, unix.SOL_IP, unix.IP_RECVORIGDSTADDR, 1); err != nil {
			return fail(err)
		}
	}
	sa := &unix.SockaddrInet4{Port: port}
	copy(sa.Addr[:], net.IPv4(127, 0, 0, 1).To4())
	if err := unix.Bind(fd, sa); err != nil {
		return fail(err)
	}
	if sotype == unix.SOCK_STREAM {
		if err := unix.Listen(fd, 128); err != nil {
			return fail(err)
		}
	}
	return fd, nil
}
