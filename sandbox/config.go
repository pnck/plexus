package sandbox

// Config holds the --sandbox tuning knobs. Every field has a default that, TAKEN
// TOGETHER, establishes the FULL sandbox — fs/namespace isolation, a deny-all network
// fence (a loopback-only netns), and a resource cgroup. The flags only ADJUST that
// default; none of them switches a feature off. `--sandbox` with no flags is a complete
// sandbox. AgentID is set by the launching command (it names the cgroup and is the
// audit key). The sandbox is established from an UNPRIVILEGED user namespace, so it
// needs no host capability — any ordinary user can start it.
type Config struct {
	AgentID string

	// Egress fence / network auditing. Defaults form a deny-all egress policy; a cluster
	// launcher (E5/CP) overrides them per agent. There is no veth/subnet: the netns is
	// loopback-only and the control plane is reached over inherited fds, so the only
	// network address that matters is the CP (default loopback, for the in-netns bus /
	// the audit proxy's own upstream carve-out).
	CP                  string
	BusPort, EgressPort int
	Relay               string
	Mark                uint32
	Table, MaxConns     int
	NetTCP, NetUDP      string

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
