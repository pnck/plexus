package cmd

import (
	"os"

	"github.com/spf13/pflag"
	"plexus/sandbox"
	"plexus/sandbox/bwrap"
	"plexus/sandbox/netpol"
)

// sandboxConfig holds the --sandbox tuning knobs. Every field has a default that,
// TAKEN TOGETHER, establishes the FULL sandbox — fs/namespace isolation, a deny-all
// network fence on a private /30, and a resource cgroup. The flags only ADJUST that
// default; none of them switches a feature off. `--sandbox` with no flags is a
// complete sandbox. AgentID is set by the launching command (it names the cgroup +
// netns and is the audit key).
type sandboxConfig struct {
	AgentID string

	// Network fence. Defaults form a self-consistent private /30 with a deny-all egress
	// policy; a cluster launcher (E5/CP) overrides them per agent.
	Netns, VethHost, VethPeer    string
	HostCIDR, AgentCIDR, Gateway string
	CP                           string
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

// addSandboxFlags registers the shared --sandbox tuning flags on a command's flag set.
// They are identical across commands: `--sandbox` means the same full sandbox
// everywhere, so its adjustment knobs are the same everywhere too.
func addSandboxFlags(fs *pflag.FlagSet, c *sandboxConfig) {
	fs.StringVar(&c.Netns, "netns", "plexus-sbx", "network namespace name")
	fs.StringVar(&c.VethHost, "veth-host", "plxh0", "host-side veth name")
	fs.StringVar(&c.VethPeer, "veth-peer", "plxa0", "agent-side veth name")
	fs.StringVar(&c.HostCIDR, "host-cidr", "10.242.42.1/30", "host-side veth address (the gateway)")
	fs.StringVar(&c.AgentCIDR, "agent-cidr", "10.242.42.2/30", "agent-side veth address")
	fs.StringVar(&c.Gateway, "gateway", "10.242.42.1", "default-route gateway inside the netns (host veth ip)")
	fs.StringVar(&c.CP, "cp", "10.242.42.1", "control-plane IPv4 (bus + relay host)")
	fs.IntVar(&c.BusPort, "bus-port", 4222, "control-plane bus port (allowed direct)")
	fs.IntVar(&c.EgressPort, "egress-port", 1080, "local transparent egress port")
	fs.StringVar(&c.Relay, "relay", "", "CP EgressRelay address host:port (empty: no upstream)")
	fs.Uint32Var(&c.Mark, "mark", 0x1, "fwmark for the TPROXY reroute")
	fs.IntVar(&c.Table, "table", 100, "routing-table id for the TPROXY reroute")
	fs.IntVar(&c.MaxConns, "max-conns", 0, "per-agent concurrent egress cap (0 = none)")
	fs.StringVar(&c.NetTCP, "net-tcp", "drop", "tcp egress: redirect|reject|drop")
	fs.StringVar(&c.NetUDP, "net-udp", "drop", "udp egress: redirect|reject|drop")
	fs.Int64Var(&c.MemMax, "mem-max", 0, "cgroup memory.max in bytes (0 = unset)")
	fs.Int64Var(&c.PidsMax, "pids-max", 0, "cgroup pids.max (0 = unset)")
	fs.IntVar(&c.UID, "uid", 0, "agent uid inside the sandbox (0 = launcher's)")
	fs.IntVar(&c.GID, "gid", 0, "agent gid inside the sandbox")
	fs.StringVar(&c.RoleCard, "role-card", "", "host path of the role card to inject read-only")
	fs.StringVar(&c.Workspace, "workspace", "", "host path of the agent workspace (writable)")
	fs.StringVar(&c.State, "state", "", "host path of the brain-private state dir (writable)")
	fs.StringVar(&c.Home, "home", "", "host path of the writable HOME")
	fs.StringSliceVar(&c.System, "ro-system", nil, "read-only base rootfs paths (default: whole /)")
	fs.StringSliceVar(&c.Mask, "mask", nil, "sensitive host paths to hide behind tmpfs")
	fs.BoolVar(&c.Clearenv, "clearenv", false, "seal the environment (only granted vars survive)")
	fs.StringSliceVar(&c.Nameservers, "nameserver", nil, "DNS nameserver IP(s); provisions a DNS-over-TCP /etc/resolv.conf")
}

// enterSandbox is the single sandbox-entry state machine, shared by every command that
// takes --sandbox. It runs the whole three-phase flow across process re-execs, keyed on
// the handover env:
//
//   - State A (fresh host launch, no handover env): establish the FULL sandbox —
//     preflight the feature set + raise caps, build the netns/nft/cgroup, and exec back
//     into this command as the Phase-0-done child. On unsupported platforms runPhase0
//     is where the clean `unimplemented` surfaces.
//   - State B (Phase-0 done, bwrap.EnvPolicy set, no ticket): reexec into the sandbox
//     provider (bwrap) with the assembled Policy.
//   - State C (inside the sandbox, ticket set): verify the ticket + self-confine, then
//     return so the caller runs the agent.
//
// States B and C are handled by the existing ticket-driven provider entry; only State A
// needs the privileged Phase-0 orchestration.
func enterSandbox(c *sandboxConfig) error {
	if os.Getenv(sandbox.EnvTicket) != "" || os.Getenv(bwrap.EnvPolicy) != "" {
		provider, err := bwrap.ProviderFromEnv()
		if err != nil {
			return err
		}
		return sandbox.EnterIfRequested(true, provider, nil)
	}
	return c.runPhase0()
}

// parseNetAction maps a CLI egress token to its NetAction (default Drop = deny).
func parseNetAction(s string) netpol.NetAction {
	switch s {
	case "redirect":
		return netpol.Redirect
	case "reject":
		return netpol.Reject
	default:
		return netpol.Drop
	}
}
