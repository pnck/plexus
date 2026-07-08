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
	fs.BoolVar(&c.RequireNetFence, "require-net-fence", false, "fail if CAP_NET_ADMIN is missing instead of degrading to host networking")
	fs.StringVar(&c.VethHost, "veth-host", "plxh0", "host-side veth name")
	fs.StringVar(&c.VethPeer, "veth-peer", "plxa0", "agent-side veth name")
	fs.StringVar(&c.HostCIDR, "host-cidr", "10.242.42.1/30", "host-side veth address (the gateway)")
	fs.StringVar(&c.AgentCIDR, "agent-cidr", "10.242.42.2/30", "agent-side veth address")
	fs.StringVar(&c.Gateway, "gateway", "10.242.42.1", "default-route gateway inside the netns (the host veth ip = CP)")
	fs.StringVar(&c.CP, "cp", "10.242.42.1", "control-plane IPv4 the fence accepts directly (bus + relay carve-out)")
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
