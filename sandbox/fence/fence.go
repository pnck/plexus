// Package fence builds the network + resource isolation an agent is born into, from
// INSIDE the fresh user+network namespace the launch stage created. It is the second
// stage of the sandbox entry chain (launch → fence → jail → confine): the process is
// ns-root of the user namespace that OWNS this network namespace, so it holds
// CAP_NET_ADMIN *scoped to the netns* for free (no host capability of its own) — enough
// to bring up loopback, configure the agent-side veth the launcher moved in, load the
// nft egress fence, install the TPROXY reroute, and open the IP_TRANSPARENT sockets. The
// netns reaches the control plane over that veth (an IP route to the gateway); the
// launcher spent host CAP_NET_ADMIN to build the veth pair. No named netns / bind-mount
// / setns is used, so CAP_SYS_ADMIN never enters.
//
// The kernel work is behind the Builder interface: the real netlink/nftables/cgroup
// implementation is swapped in at runtime (Linux), while the orchestration SEQUENCE is
// unit-tested against a recording fake.
package fence

import (
	"fmt"

	"plexus/sandbox/netpol"
)

// Limits are the per-agent resource ceilings written into the cgroup. A zero field
// means "leave unset". When cgroup delegation is unavailable the Builder degrades to
// the rlimit floor applied later in the confine stage.
type Limits struct {
	MemoryMax int64 // memory.max, bytes; 0 = unset
	PidsMax   int64 // pids.max; 0 = unset
}

// Plan is the startup-fixed input Build needs to fence one agent. Every value comes
// from the E5 catalog (CP address, limits, the agent's NetPolicy); the MECHANISM that
// turns them into kernel objects is here. A Plan is consumed once and never mutated.
type Plan struct {
	AgentID string // names the cgroup; also the audit/attribution key

	// Agent-side veth (the launcher already created the pair and moved this peer into the
	// netns using CAP_NET_ADMIN). The fence gives it AgentCIDR and a single default route
	// to Gateway (the host veth = the control plane), so egress reaches only the CP.
	VethPeer  string
	AgentCIDR string
	Gateway   string

	// Egress fence. Net is the immutable startup NetPolicy; NFT carries the CP/bus/
	// egress-port/mark/table/maxconns the generators need. Both the nft ruleset and the
	// TPROXY ip-rules are produced from these and applied to the netns.
	Net netpol.NetPolicy
	NFT netpol.Params

	Limits Limits

	// Agent is the process to launch once the fence + cgroup are ready: `plexus <cmd>
	// --sandbox` carrying the assembled bwrap Policy in Env but no ticket, so it enters
	// the jail stage. Its parent stays the launcher (the host-netns supervisor), so
	// --die-with-parent anchors correctly.
	Agent Cmd
}

// Cmd is the argv+env of the agent to spawn once the fence is up.
type Cmd struct {
	Argv []string
	Env  []string
}

// Builder performs the fence operations, all inside the CURRENT (userns-owned) network
// namespace — there is no netns argument because the process is already in the target
// netns. Every method is fail-fast: an error aborts Build before
// the agent is spawned. The real implementation is Linux-only and needs only the
// in-userns CAP_NET_ADMIN (no host capability) plus, optionally, a delegated cgroup
// subtree; tests drive a recording fake.
type Builder interface {
	// UpLoopback brings the netns's own lo up — it starts DOWN, and the fence accepts
	// 127.0.0.0/8, the TPROXY reroute delivers `dev lo`, and the egress proxy binds
	// 127.0.0.1, all of which need it up.
	UpLoopback() error
	// SetupVeth configures the agent-side veth peer the launcher moved into this netns:
	// its address (cidr) + up + the single default route to gateway (the host veth = the
	// control plane). It gives locally-generated egress a route so it reaches the output
	// hook (where TPROXY marks it) and the CP is reachable for the bus/relay carve-out.
	SetupVeth(peerIface, cidr, gateway string) error
	// ApplyEgressFence installs the egress fence from the immutable NetPolicy + Params:
	// the nft ruleset (deny-all, bus-direct, redirect→TPROXY mark, ct count) and, when a
	// protocol is redirected (auditing on), the TPROXY reroute — the fwmark rule and the
	// `local default dev lo` table. The first output-route lookup is carried by the veth
	// default route SetupVeth installed, so no base default-via-lo route is needed.
	ApplyEgressFence(policy netpol.NetPolicy, params netpol.Params) error
	// LimitResources creates the per-agent cgroup and writes its ceilings, degrading to
	// the rlimit floor (a nil error) when no cgroup subtree is delegated.
	LimitResources(agentID string, lim Limits) error
	// SpawnAgent opens the IP_TRANSPARENT egress sockets (when egressPort > 0) in the
	// current netns — while the process still holds the in-userns CAP_NET_ADMIN — passes
	// them to the agent as inherited fds, then execs it (replacing the process, so it
	// does not return on success). The confined agent cannot open those sockets itself;
	// it only serves the inherited fds. No setns: the process is already in the netns.
	SpawnAgent(egressPort int, agent Cmd) error
}

// Build fences one agent inside the userns-owned netns:
//
//  1. validate the fence params (pure, fail-closed) — never build a kernel object for
//     a plan whose nft / ip-rules we cannot even produce;
//  2. bring the netns loopback up;
//  3. apply the nft fence + (when auditing redirects a protocol) the TPROXY reroute;
//  4. create the resource cgroup;
//  5. open the egress sockets and exec the agent.
//
// It is fail-closed and fail-fast: any error aborts before (or during) the build and
// the agent is never spawned. There is nothing to unwind — the netns is anonymous and
// evaporates when this process exits, and the cgroup lives under our own delegated
// subtree — so a failed step just returns and the launch supervisor sees the exit.
func Build(p Plan, b Builder) error {
	if err := p.NFT.Validate(); err != nil {
		return fmt.Errorf("fence %s: %w", p.AgentID, err)
	}
	steps := []struct {
		what string
		do   func() error
	}{
		{"up loopback", b.UpLoopback},
		{"configure agent veth", func() error { return b.SetupVeth(p.VethPeer, p.AgentCIDR, p.Gateway) }},
		{"apply egress fence", func() error { return b.ApplyEgressFence(p.Net, p.NFT) }},
		{"limit resources", func() error { return b.LimitResources(p.AgentID, p.Limits) }},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			return fmt.Errorf("fence %s: %s: %w", p.AgentID, s.what, err)
		}
	}
	if err := b.SpawnAgent(p.NFT.EgressPort, p.Agent); err != nil {
		return fmt.Errorf("fence %s: spawn agent: %w", p.AgentID, err)
	}
	return nil
}
