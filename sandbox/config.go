package sandbox

import (
	"fmt"
	"hash/fnv"
)

// Config holds the --sandbox tuning knobs. Every field has a default that, TAKEN
// TOGETHER, establishes the FULL sandbox — fs/namespace isolation, a deny-all network
// fence (per-agent netns + veth to the CP), and a resource cgroup. The flags only ADJUST
// that default; none of them switches a feature off. `--sandbox` with no flags is a
// complete sandbox. AgentID is set by the launching command (it names the cgroup and is
// the audit key). The core sandbox is established from an UNPRIVILEGED user namespace
// (zero host capability); the network fence additionally needs CAP_NET_ADMIN and
// degrades gracefully without it (§ implement-design 5.6.9).
type Config struct {
	AgentID string

	// RequireNetFence turns the network fence from best-effort into mandatory: when set
	// and CAP_NET_ADMIN is absent, Enter errors instead of degrading to host networking.
	RequireNetFence bool

	// Network fence (needs CAP_NET_ADMIN). The veth's peer end is moved into the agent
	// netns with AgentCIDR; the host end keeps HostCIDR and is the agent's only gateway,
	// so the single default route (to Gateway) reaches nothing but the control plane.
	VethHost, VethPeer           string
	HostCIDR, AgentCIDR, Gateway string
	CP                           string // the CP IPv4 the fence accepts directly (bus + relay carve-out)
	BusPort, EgressPort          int
	Relay                        string
	Mark                         uint32
	Table, MaxConns              int
	NetTCP, NetUDP               string

	// Resource cgroup (0 = create the group but leave that limit unset).
	MemMax, PidsMax int64

	// Identity inside the user namespace (0 = the launcher's mapping).
	UID, GID int

	// Filesystem Policy. Empty provision Srcs are skipped; empty System => whole "/".
	RoleCard, Workspace, State, Home string
	System, Mask                     []string
	Clearenv                         bool
	Nameservers                      []string
}

// DefaultConfig returns a Config populated with the load-bearing sandbox defaults — the
// veth names, per-agent CIDRs, gateway, control-plane address, egress port, fwmark, and
// routing table — that, TAKEN TOGETHER, establish the full sandbox. It is the single
// source of truth for these values: addSandboxFlags seeds its flag defaults from here, so
// a Config built programmatically (not via the flag set) is equally valid and does not
// fail at SetupVeth with empty veth/CIDR fields. Callers set AgentID and may override any
// field. The zero-value knobs it deliberately leaves unset (MemMax/PidsMax/MaxConns = 0
// "unset", UID/GID = 0 "launcher's mapping", provision paths = "" "skip") keep their
// zero meaning.
func DefaultConfig() Config {
	return Config{
		VethHost:   "plxh0",
		VethPeer:   "plxa0",
		HostCIDR:   "10.242.42.1/30",
		AgentCIDR:  "10.242.42.2/30",
		Gateway:    "10.242.42.1",
		CP:         "10.242.42.1",
		BusPort:    4222,
		EgressPort: 1080,
		Mark:       0x1,
		Table:      100,
		NetTCP:     "drop",
		NetUDP:     "drop",
	}
}

// deriveAgentNet gives each agent its own veth pair + /30 so concurrent sandboxes don't
// collide on the shared default (plxh0/plxa0 + 10.242.42.0/30 — a second agent would hit
// "file exists" on LinkAdd and duplicate addresses). It only rewrites the net fields still
// at the DefaultConfig base, so an explicit --veth-host/--agent-cidr/… override is kept.
//
// The mapping is DETERMINISTIC in AgentID: the launcher (which builds the veth) and the
// re-exec'd fence stage (which configures the peer) run this independently and must agree
// on the names/addresses. The pool is 10.242.0.0/16 carved into /30s (16384 agents) keyed
// by an FNV hash of AgentID; the host end is the gateway (.1 of the block), the agent end
// .2. Distinct AgentIDs can still birthday-collide on a slot — a live-set allocator is
// future work; pass explicit net flags to pin an agent.
func (c *Config) deriveAgentNet() {
	if c.AgentID == "" {
		return
	}
	base := DefaultConfig()
	if c.VethHost != base.VethHost || c.VethPeer != base.VethPeer ||
		c.HostCIDR != base.HostCIDR || c.AgentCIDR != base.AgentCIDR || c.Gateway != base.Gateway {
		return // caller pinned the net explicitly — leave it
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(c.AgentID))
	idx := h.Sum32() % 16384 // 2^14 /30 blocks in a /16
	off := idx * 4           // block base offset within 10.242.0.0/16
	third, fourth := byte(off>>8), byte(off&0xff)

	gw := fmt.Sprintf("10.242.%d.%d", third, fourth+1)
	c.Gateway = gw
	c.HostCIDR = gw + "/30"
	c.AgentCIDR = fmt.Sprintf("10.242.%d.%d/30", third, fourth+2)
	c.VethHost = fmt.Sprintf("plxh%x", idx) // ≤ 8 chars, within IFNAMSIZ
	c.VethPeer = fmt.Sprintf("plxa%x", idx)
	if c.CP == base.CP { // CP defaults to the host veth (gateway) unless pinned
		c.CP = gw
	}
}
