package cmd

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"plexus/sandbox/caps"
	"plexus/sandbox/egress"
	"plexus/sandbox/netpol"
	"plexus/sandbox/setup"
)

var (
	setupAgentID    string
	setupNetns      string
	setupVethHost   string
	setupVethPeer   string
	setupHostCIDR   string
	setupAgentCIDR  string
	setupGateway    string
	setupCP         string
	setupBusPort    int
	setupEgressPort int
	setupRelay      string
	setupMark       uint32
	setupTable      int
	setupMaxConns   int
	setupNetTCP     string
	setupNetUDP     string
	setupMemMax     int64
	setupPidsMax    int64
	setupUID        int
	setupGID        int
)

// setupCmd is the privileged Phase-0 entry (flow doc §2): it centrally checks/raises
// the required capabilities, builds the per-agent netns + egress fence + cgroup from
// its flags, and execs the agent (which self-reexecs into bwrap). Usually invoked by
// the control plane / a supervisor per the E5 catalog; the flags are the "values".
// Needs CAP_NET_ADMIN + CAP_SYS_ADMIN (raised at startup if permitted).
var setupCmd = &cobra.Command{
	Use:   "setup [flags] -- <agent command...>",
	Short: "Phase-0 privileged sandbox setup: build the netns + egress fence + cgroup, then exec the agent",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("setup: provide the agent command after --, e.g. `plexus setup … -- plexus run --sandbox`")
		}

		// capset raises EFFECTIVE on the calling THREAD only, so lock the goroutine to
		// its OS thread and keep all the privileged setup on it — otherwise Go could
		// migrate the goroutine to a thread whose caps were never raised (invisible as
		// root, but it breaks the non-root `setcap …+p` deployment).
		runtime.LockOSThread()

		// Central, up-front capability check: union every participant's needs (the
		// visitor over Setup + the egress proxy) and raise them once.
		if err := caps.Ensure(setup.RequiredCaps().Union((&egress.Proxy{}).RequiredCaps())); err != nil {
			return err
		}

		x, err := setup.NewExecutor()
		if err != nil {
			return err
		}
		plan := setup.Plan{
			AgentID:   setupAgentID,
			Netns:     setupNetns,
			VethHost:  setupVethHost,
			VethPeer:  setupVethPeer,
			HostCIDR:  setupHostCIDR,
			AgentCIDR: setupAgentCIDR,
			Gateway:   setupGateway,
			Net:       netpol.NetPolicy{TCP: parseNetAction(setupNetTCP), UDP: parseNetAction(setupNetUDP)},
			NFT: netpol.Params{
				CP: setupCP, BusPort: setupBusPort, EgressPort: setupEgressPort,
				Mark: setupMark, Table: setupTable, MaxConns: setupMaxConns,
			},
			Cgroup: setup.CgroupLimits{MemoryMax: setupMemMax, PidsMax: setupPidsMax},
			Uid:    setupUID,
			Gid:    setupGID,
			Argv:   args,
			// The agent reads the relay + per-protocol policy from the environment; the
			// transparent socket fds are added by EnterAndExec.
			Env: append(os.Environ(),
				egress.EnvRelay+"="+setupRelay,
				egress.EnvNetTCP+"="+setupNetTCP,
				egress.EnvNetUDP+"="+setupNetUDP,
			),
		}
		return setup.Setup(plan, x) // execs the agent on success; returns only on error
	},
}

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

func init() {
	rootCmd.AddCommand(setupCmd)
	f := setupCmd.Flags()
	f.StringVar(&setupAgentID, "id", "agent-x", "agent id (cgroup name + audit key)")
	f.StringVar(&setupNetns, "netns", "", "network namespace name")
	f.StringVar(&setupVethHost, "veth-host", "", "host-side veth name")
	f.StringVar(&setupVethPeer, "veth-peer", "", "agent-side veth name")
	f.StringVar(&setupHostCIDR, "host-cidr", "", "host-side veth address, e.g. 10.0.0.1/30")
	f.StringVar(&setupAgentCIDR, "agent-cidr", "", "agent-side veth address, e.g. 10.0.0.2/30")
	f.StringVar(&setupGateway, "gateway", "", "default-route gateway inside the netns (host veth ip)")
	f.StringVar(&setupCP, "cp", "", "control-plane IPv4 (bus + relay host)")
	f.IntVar(&setupBusPort, "bus-port", 4222, "control-plane bus port (allowed direct)")
	f.IntVar(&setupEgressPort, "egress-port", 1080, "local transparent egress port")
	f.StringVar(&setupRelay, "relay", "", "CP EgressRelay address, host:port")
	f.Uint32Var(&setupMark, "mark", 0x1, "fwmark for the TPROXY reroute")
	f.IntVar(&setupTable, "table", 100, "routing-table id for the TPROXY reroute")
	f.IntVar(&setupMaxConns, "max-conns", 0, "per-agent concurrent egress cap (0 = none)")
	f.StringVar(&setupNetTCP, "net-tcp", "drop", "tcp egress: redirect|reject|drop")
	f.StringVar(&setupNetUDP, "net-udp", "drop", "udp egress: redirect|reject|drop")
	f.Int64Var(&setupMemMax, "mem-max", 0, "cgroup memory.max in bytes (0 = unset)")
	f.Int64Var(&setupPidsMax, "pids-max", 0, "cgroup pids.max (0 = unset)")
	f.IntVar(&setupUID, "uid", 0, "agent uid inside the sandbox (0 = launcher's)")
	f.IntVar(&setupGID, "gid", 0, "agent gid inside the sandbox")
}
