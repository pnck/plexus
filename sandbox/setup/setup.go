// Package setup is the sandbox Phase 0 orchestration (E4.6.4) — the one privileged
// stage that builds everything an agent cannot build once confined: the per-agent
// network namespace + veth (route only to the control plane), the nft egress fence
// + TPROXY reroute generated from the agent's NetPolicy, and the resource cgroup;
// then it enters that netns + cgroup and execs the agent (which self-reexecs into
// bwrap for Phase 1). See tracking/E4-establishment-flow.md §2 — this code wires
// that flow and must not deviate from it.
//
// The privileged operations are behind the Executor interface: the real
// netlink/nftables/cgroups implementation (Linux + CAP_NET_ADMIN + cgroup
// delegation) is swapped in for privileged environments (E4.6.4.2, 🔒), while the
// orchestration SEQUENCE is unit-tested here against a recording fake (E4.6.4.1).
package setup

import (
	"fmt"

	"plexus/sandbox/caps"
	"plexus/sandbox/netpol"
)

// RequiredCaps reports the host capabilities Phase-0 Setup needs: CAP_NET_ADMIN for
// the netns/veth/route/nft config (and the IP_TRANSPARENT egress sockets),
// CAP_SYS_ADMIN to create and mount the network namespace. The launcher unions this
// with the other participants' needs (caps.Collect) and raises them once at startup
// (caps.Ensure) before running Setup — the central, up-front capability check.
func RequiredCaps() caps.Set { return caps.Of(caps.NetAdmin, caps.SysAdmin) }

// CgroupLimits are the per-agent resource ceilings written into the cgroup (values
// from the E5 catalog). A zero field means "leave unset". When cgroup delegation is
// unavailable, the real Executor degrades to the rlimit floor (flow doc §7); that
// degradation is the Executor's concern, not the orchestration's.
type CgroupLimits struct {
	MemoryMax int64  // memory.max, bytes; 0 = unset
	PidsMax   int64  // pids.max; 0 = unset
	CPUMax    string // cpu.max, "quota period"; "" = unset
}

// Plan is the startup-fixed input Setup needs to build one agent's Phase 0
// isolation. Every value comes from the E5 catalog (CP address, subnet, limits,
// the agent's NetPolicy); the MECHANISM that turns them into kernel objects is
// here. A Plan is consumed once and never mutated.
type Plan struct {
	AgentID string // names the cgroup; also the audit/attribution key

	// Network namespace + veth. The peer end is moved into the netns and given
	// AgentCIDR; the host end keeps HostCIDR and is the agent's only gateway, so the
	// single default route (to Gateway) reaches nothing but the control plane.
	Netns     string
	VethHost  string
	VethPeer  string
	HostCIDR  string // host-side veth address (the gateway), e.g. "10.0.0.1/30"
	AgentCIDR string // agent-side veth address, e.g. "10.0.0.2/30"
	Gateway   string // default-route target inside the netns (host veth IP)

	// Egress fence. Net is the immutable startup NetPolicy; NFT carries the CP/bus/
	// egress-port/mark/table/maxconns the generators need. Both nft ruleset and the
	// TPROXY ip-rules are produced from these (E4.5.2) and applied to the netns.
	Net netpol.NetPolicy
	NFT netpol.Params

	Cgroup CgroupLimits

	// Uid/Gid are the identity the agent runs as inside the sandbox. This is an
	// EXTERNAL launch parameter — identity, not a capability — so it is passed in
	// (from the E5 catalog) rather than acquired. 0 keeps the launcher's id; a
	// non-zero value is threaded to bwrap's user-namespace mapping in Phase 1.
	Uid int
	Gid int

	// The agent to launch once its netns + cgroup are ready: `plexus run … --sandbox`
	// WITHOUT a ticket, so it enters the self-reexec path (Phase 1). Its parent stays
	// the persistent CP, so --die-with-parent anchors correctly.
	Argv []string
	Env  []string
}

// Executor performs the privileged Phase 0 operations. netns == "" targets the host
// namespace. Every method is fail-fast: an error aborts Setup before the agent is
// spawned. The real implementation is Linux-only and needs CAP_NET_ADMIN + cgroup
// delegation (E4.6.4.2); tests drive a recording fake.
type Executor interface {
	CreateNetns(name string) error
	CreateVethPair(hostIface, agentIface string) error
	MoveToNetns(iface, netns string) error
	SetAddr(netns, iface, cidr string) error
	SetLinkUp(netns, iface string) error
	AddDefaultRoute(netns, gateway string) error
	// ApplyFence installs the egress fence into the netns from the immutable
	// NetPolicy + Params: the nft ruleset (deny-all, bus-direct, redirect→TPROXY
	// mark, ct count) and, for a redirected protocol, the TPROXY reroute (fwmark
	// rule + local route). The real executor drives nftables + netlink directly
	// (netpol.GenerateNFT/GenerateIPRules are the golden text it mirrors); the fake
	// records the (policy, params) it was asked to apply.
	ApplyFence(netns string, policy netpol.NetPolicy, params netpol.Params) error
	CreateCgroup(name string, lim CgroupLimits) error
	// EnterAndExec joins the netns + cgroup and execs argv (replacing the process, so
	// it does not return on success). The child's parent stays the caller (the CP).
	EnterAndExec(netns, cgroup string, argv, env []string) error
}

// Setup runs the Phase 0 sequence for one agent (flow doc §1 timeline / §2):
//
//  1. generate the egress fence text (pure, fail-closed) — never build a kernel
//     object for a plan whose nft / ip-rules we cannot even produce;
//  2. net: netns + veth, addresses, and the single default route to the CP;
//  3. apply the nft fence + (for a redirected protocol) the TPROXY reroute;
//  4. create the resource cgroup;
//  5. enter the netns + cgroup and exec the agent.
//
// It is fail-closed and fail-fast: any error aborts before (or during) the build
// and the agent is never spawned, so a half-built fence never runs an agent.
func Setup(p Plan, x Executor) error {
	// (1) Fail-closed: validate the fence params before any kernel object exists — a
	// bad plan (e.g. a CP that is not a bare IPv4, which could inject nft rules)
	// builds nothing.
	if err := p.NFT.Validate(); err != nil {
		return fmt.Errorf("setup %s: %w", p.AgentID, err)
	}

	// (2) Network namespace + veth (peer in the netns with the agent address, host
	// end the gateway, single route to the CP); (3) the egress fence built from the
	// NetPolicy; (4) the resource cgroup.
	steps := []struct {
		what string
		do   func() error
	}{
		{"create netns", func() error { return x.CreateNetns(p.Netns) }},
		{"create veth", func() error { return x.CreateVethPair(p.VethHost, p.VethPeer) }},
		{"move veth to netns", func() error { return x.MoveToNetns(p.VethPeer, p.Netns) }},
		{"addr host veth", func() error { return x.SetAddr("", p.VethHost, p.HostCIDR) }},
		{"up host veth", func() error { return x.SetLinkUp("", p.VethHost) }},
		{"addr agent veth", func() error { return x.SetAddr(p.Netns, p.VethPeer, p.AgentCIDR) }},
		{"up agent veth", func() error { return x.SetLinkUp(p.Netns, p.VethPeer) }},
		{"default route to CP", func() error { return x.AddDefaultRoute(p.Netns, p.Gateway) }},
		{"apply egress fence", func() error { return x.ApplyFence(p.Netns, p.Net, p.NFT) }},
		{"create cgroup", func() error { return x.CreateCgroup(p.AgentID, p.Cgroup) }},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			return fmt.Errorf("setup %s: %s: %w", p.AgentID, s.what, err)
		}
	}

	// (5) Enter the prepared netns + cgroup and exec the agent (replaces the process).
	if err := x.EnterAndExec(p.Netns, p.AgentID, p.Argv, p.Env); err != nil {
		return fmt.Errorf("setup %s: enter+exec: %w", p.AgentID, err)
	}
	return nil
}
