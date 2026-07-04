package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/spf13/cobra"
	"plexus/sandbox/bwrap"
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

	// Phase-1 bwrap Policy (fs / provision / env) — the values Setup assembles and
	// hands to the agent via bwrap.EnvPolicy.
	setupRoleCard    string
	setupWorkspace   string
	setupState       string
	setupHome        string
	setupSystem      []string
	setupMask        []string
	setupClearenv    bool
	setupNameservers []string
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

		// Assemble the Phase-1 bwrap Policy (fs view + provision + sealed env) and hand
		// it to the agent's self-reexec via bwrap.EnvPolicy. System defaults to the
		// whole rootfs (dev) unless narrowed with --ro-system. Empty provision Srcs are
		// skipped by Translate.
		system := setupSystem
		if len(system) == 0 {
			system = []string{"/"}
		}
		provision := bwrap.Provision{
			RoleCard:  bwrap.Bind{Src: setupRoleCard},
			State:     bwrap.Bind{Src: setupState},
			Workspace: bwrap.Bind{Src: setupWorkspace},
			Home:      bwrap.Bind{Src: setupHome},
		}
		// DNS-over-TCP resolv.conf, so a udp:drop role still resolves. Generated here
		// and bound read-only into the sandbox at /etc/resolv.conf.
		if len(setupNameservers) > 0 {
			rc, err := netpol.ResolvConf(setupNameservers)
			if err != nil {
				return err
			}
			path := filepath.Join(os.TempDir(), "plexus-setup-"+setupAgentID+"-resolv.conf")
			if err := os.WriteFile(path, []byte(rc), 0o644); err != nil {
				return fmt.Errorf("setup: write resolv.conf: %w", err)
			}
			provision.ResolvConf = bwrap.Bind{Src: path}
		}
		policyJSON, err := json.Marshal(bwrap.Policy{
			System:    system,
			Mask:      setupMask,
			Clearenv:  setupClearenv,
			Provision: provision,
			Uid:       setupUID,
			Gid:       setupGID,
		})
		if err != nil {
			return fmt.Errorf("setup: marshal policy: %w", err)
		}

		// The relay port carves the proxy's own upstream out of the fence (no TPROXY loop).
		relayPort := 0
		if _, p, err := net.SplitHostPort(setupRelay); err == nil {
			relayPort, _ = strconv.Atoi(p)
		}

		// The agent reads the relay + per-protocol policy + fs Policy from the env; only
		// override the relay when set (an empty append would blank an inherited value).
		agentEnv := append(os.Environ(),
			egress.EnvNetTCP+"="+setupNetTCP,
			egress.EnvNetUDP+"="+setupNetUDP,
			bwrap.EnvPolicy+"="+string(policyJSON),
		)
		if setupRelay != "" {
			agentEnv = append(agentEnv, egress.EnvRelay+"="+setupRelay)
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
				CP: setupCP, BusPort: setupBusPort, RelayPort: relayPort, EgressPort: setupEgressPort,
				Mark: setupMark, Table: setupTable, MaxConns: setupMaxConns,
			},
			Cgroup: setup.CgroupLimits{MemoryMax: setupMemMax, PidsMax: setupPidsMax},
			Argv:   args,
			Env:    agentEnv, // transparent socket fds are appended by EnterAndExec
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
	// Phase-1 fs Policy (handed to the agent via bwrap.EnvPolicy).
	f.StringVar(&setupRoleCard, "role-card", "", "host path of the role card to inject read-only")
	f.StringVar(&setupWorkspace, "workspace", "", "host path of the agent workspace (writable)")
	f.StringVar(&setupState, "state", "", "host path of the brain-private state dir (writable)")
	f.StringVar(&setupHome, "home", "", "host path of the writable HOME")
	f.StringSliceVar(&setupSystem, "ro-system", nil, "read-only base rootfs paths (default: whole /)")
	f.StringSliceVar(&setupMask, "mask", nil, "sensitive host paths to hide behind tmpfs")
	f.BoolVar(&setupClearenv, "clearenv", false, "seal the environment (only granted vars survive)")
	f.StringSliceVar(&setupNameservers, "nameserver", nil, "DNS nameserver IP(s); provisions a DNS-over-TCP /etc/resolv.conf")
}
