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
