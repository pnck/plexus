//go:build linux

package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"

	"plexus/sandbox"
	"plexus/sandbox/bwrap"
	"plexus/sandbox/egress"
	"plexus/sandbox/netpol"
	"plexus/sandbox/setup"
)

// runPhase0 is the fresh-host-launch (State A) body of --sandbox on the supported
// linux/bwrap backend: it probes the FULL feature set + raises the required caps, then
// builds the per-agent netns + egress fence + cgroup and execs back into this same
// command as the Phase-0-done child (which reexecs into bwrap). It returns only on
// error — on success it has replaced the process.
func (c *sandboxConfig) runPhase0() error {
	// capset raises EFFECTIVE on the calling THREAD only, so lock the goroutine to its
	// OS thread and keep all the privileged setup on it — otherwise Go could migrate the
	// goroutine to a thread whose caps were never raised.
	runtime.LockOSThread()

	// The full required-capability set, unioned from every privileged participant (the
	// visitor over Setup + the egress proxy). Preflight probes the whole feature set
	// (userns + these caps) once, up front, and raises the caps on success.
	required := setup.RequiredCaps().Union((&egress.Proxy{}).RequiredCaps())
	if err := sandbox.Preflight(required); err != nil {
		return err
	}

	x, err := setup.NewExecutor()
	if err != nil {
		return err
	}
	plan, err := c.plan(os.Args)
	if err != nil {
		return err
	}
	return setup.Setup(plan, x) // enters the netns+cgroup and execs the child; returns only on error
}

// plan assembles the Phase-0 Plan from the (defaulted) sandbox config: the netns/veth
// addressing, the egress fence (deny-all by default), the cgroup limits, the bwrap fs
// Policy (handed down via bwrap.EnvPolicy), and the child argv to exec once the netns +
// cgroup are ready. argv is this process's own os.Args, so the child re-enters the same
// command as the Phase-0-done stage.
func (c *sandboxConfig) plan(argv []string) (setup.Plan, error) {
	system := c.System
	if len(system) == 0 {
		system = []string{"/"}
	}
	provision := bwrap.Provision{
		RoleCard:  bwrap.Bind{Src: c.RoleCard},
		State:     bwrap.Bind{Src: c.State},
		Workspace: bwrap.Bind{Src: c.Workspace},
		Home:      bwrap.Bind{Src: c.Home},
	}
	// DNS-over-TCP resolv.conf, so a udp:drop policy still resolves. Generated here and
	// bound read-only into the sandbox at /etc/resolv.conf.
	if len(c.Nameservers) > 0 {
		rc, err := netpol.ResolvConf(c.Nameservers)
		if err != nil {
			return setup.Plan{}, err
		}
		f, err := os.CreateTemp("", "plexus-resolv-*.conf")
		if err != nil {
			return setup.Plan{}, fmt.Errorf("sandbox: create resolv.conf: %w", err)
		}
		if _, err := f.WriteString(rc); err != nil {
			_ = f.Close()
			return setup.Plan{}, fmt.Errorf("sandbox: write resolv.conf: %w", err)
		}
		_ = f.Close()
		provision.ResolvConf = bwrap.Bind{Src: f.Name()}
	}
	policyJSON, err := json.Marshal(bwrap.Policy{
		System:    system,
		Mask:      c.Mask,
		Clearenv:  c.Clearenv,
		Provision: provision,
		Uid:       c.UID,
		Gid:       c.GID,
	})
	if err != nil {
		return setup.Plan{}, fmt.Errorf("sandbox: marshal policy: %w", err)
	}

	// The relay port carves the proxy's own upstream out of the fence (no TPROXY loop).
	relayPort := 0
	if _, p, err := net.SplitHostPort(c.Relay); err == nil {
		relayPort, _ = strconv.Atoi(p)
	}

	env := append(os.Environ(),
		egress.EnvNetTCP+"="+c.NetTCP,
		egress.EnvNetUDP+"="+c.NetUDP,
		bwrap.EnvPolicy+"="+string(policyJSON),
	)
	if c.Relay != "" {
		env = append(env, egress.EnvRelay+"="+c.Relay)
	}

	return setup.Plan{
		AgentID:   c.AgentID,
		Netns:     c.Netns,
		VethHost:  c.VethHost,
		VethPeer:  c.VethPeer,
		HostCIDR:  c.HostCIDR,
		AgentCIDR: c.AgentCIDR,
		Gateway:   c.Gateway,
		Net:       netpol.NetPolicy{TCP: parseNetAction(c.NetTCP), UDP: parseNetAction(c.NetUDP)},
		NFT: netpol.Params{
			CP: c.CP, BusPort: c.BusPort, RelayPort: relayPort, EgressPort: c.EgressPort,
			Mark: c.Mark, Table: c.Table, MaxConns: c.MaxConns,
		},
		Cgroup: setup.CgroupLimits{MemoryMax: c.MemMax, PidsMax: c.PidsMax},
		Argv:   argv,
		Env:    env,
	}, nil
}
