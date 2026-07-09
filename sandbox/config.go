package sandbox

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
