package cmd

import (
	"github.com/spf13/pflag"
	"plexus/sandbox"
)

// addSandboxFlags registers the shared --sandbox tuning flags on a command's flag set,
// binding them to a sandbox.Config. They are identical across commands: `--sandbox`
// means the same full sandbox everywhere, so its adjustment knobs are the same too. The
// defaults, taken together, establish the complete sandbox; the flags only ADJUST it.
func addSandboxFlags(fs *pflag.FlagSet, c *sandbox.Config) {
	d := sandbox.DefaultConfig() // single source of truth for the net defaults
	fs.BoolVar(&c.RequireNetFence, "require-net-fence", false, "fail if CAP_NET_ADMIN is missing instead of degrading to host networking")
	fs.StringVar(&c.VethHost, "veth-host", d.VethHost, "host-side veth name")
	fs.StringVar(&c.VethPeer, "veth-peer", d.VethPeer, "agent-side veth name")
	fs.StringVar(&c.HostCIDR, "host-cidr", d.HostCIDR, "host-side veth address (the gateway)")
	fs.StringVar(&c.AgentCIDR, "agent-cidr", d.AgentCIDR, "agent-side veth address")
	fs.StringVar(&c.Gateway, "gateway", d.Gateway, "default-route gateway inside the netns (the host veth ip = CP)")
	fs.StringVar(&c.CP, "cp", d.CP, "control-plane IPv4 the fence accepts directly (bus + relay carve-out)")
	fs.IntVar(&c.BusPort, "bus-port", d.BusPort, "control-plane bus port (allowed direct)")
	fs.IntVar(&c.EgressPort, "egress-port", d.EgressPort, "local transparent egress port")
	fs.StringVar(&c.Relay, "relay", "", "CP EgressRelay address host:port (empty: no upstream)")
	fs.Uint32Var(&c.Mark, "mark", d.Mark, "fwmark for the TPROXY reroute")
	fs.IntVar(&c.Table, "table", d.Table, "routing-table id for the TPROXY reroute")
	fs.IntVar(&c.MaxConns, "max-conns", 0, "per-agent concurrent egress cap (0 = none)")
	fs.StringVar(&c.NetTCP, "net-tcp", d.NetTCP, "tcp egress: redirect|reject|drop")
	fs.StringVar(&c.NetUDP, "net-udp", d.NetUDP, "udp egress: redirect|reject|drop")
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
