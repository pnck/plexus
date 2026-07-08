//go:build linux

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"plexus/sandbox/bwrap"
	"plexus/sandbox/egress"
	"plexus/sandbox/fence"
	"plexus/sandbox/netpol"
)

// provider returns the linux filesystem-jail backend: bwrap, configured from the Policy
// the fence stage handed down (or the permissive default when none was set).
func provider() (Provider, error) { return bwrap.ProviderFromEnv() }

// launch (stage 1) forks THIS command — same argv — into a fresh user+network namespace
// and supervises it, using ZERO host capabilities. Mapping our uid/gid to 0 makes the
// child ns-root of the user namespace that OWNS the new netns, which grants CAP_NET_ADMIN
// scoped to that netns for free — all the fence stage needs. The launcher stays in the
// host netns as a thin supervisor (signal funnel now, home of the CP connect-broker
// later — identical for cluster and standalone) and propagates the child's exit. It
// returns only on a spawn error; otherwise it exits with the child's status.
func launch(_ Config) error {
	// Exec THIS binary via /proc/self/exe (independent of PATH/cwd) but keep the original
	// argv[0]: it propagates down the chain as the agent's argv, and later stages (the
	// fence's SpawnAgent, bwrap's `-- os.Args`) must exec the real binary path, not
	// "/proc/self/exe", which does not resolve inside bwrap's mount namespace.
	cmd := exec.Command("/proc/self/exe", os.Args[1:]...)
	cmd.Args[0] = os.Args[0]
	cmd.Env = append(os.Environ(), envStage+"="+stageFenced)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false, // an unprivileged gid map requires setgroups=deny
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("sandbox: entering an unprivileged user+network namespace failed (%w) — "+
			"`--sandbox` builds the whole sandbox from an unprivileged userns and needs it enabled; "+
			"check `sysctl kernel.unprivileged_userns_clone` / `user.max_user_namespaces`, or run on a "+
			"host/container that permits unprivileged user namespaces", err)
	}

	forwardSignals(cmd.Process)
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// Propagate the child's status faithfully: its exit code, or 128+signo when it
			// was killed by a signal (ExitCode() is -1 there, which a shell renders as 255).
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				os.Exit(128 + int(ws.Signal()))
			}
			os.Exit(ee.ExitCode())
		}
		return fmt.Errorf("sandbox supervisor: %w", err)
	}
	os.Exit(0)
	return nil // unreachable
}

// forwardSignals makes the host-netns supervisor relay service-teardown signals to the
// sandbox child. SIGINT is deliberately ignored here (not forwarded): the child shares
// the terminal's process group, so an interactive Ctrl-C already reaches it directly —
// forwarding would double-deliver. SIGTERM/SIGHUP/SIGQUIT (a service manager killing the
// supervisor pid) ARE forwarded so the child tears down with its parent.
func forwardSignals(p *os.Process) {
	signal.Ignore(syscall.SIGINT)
	sigc := make(chan os.Signal, 4)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		for s := range sigc {
			if ss, ok := s.(syscall.Signal); ok {
				_ = p.Signal(ss)
			}
		}
	}()
}

// buildFence (stage 2) runs inside the fresh user+network namespace: it assembles the
// plan (fence params + the bwrap Policy handed down via env) and builds the network
// fence + cgroup, then execs the agent onward into the jail stage. It returns only on
// error. The kernel work stays on one locked OS thread.
func buildFence(cfg Config) error {
	runtime.LockOSThread()
	plan, err := planFor(cfg)
	if err != nil {
		return err
	}
	return fence.Build(plan, fence.NewOSBuilder())
}

// planFor assembles the fence Plan from the (defaulted) Config: the egress fence
// (deny-all by default), the cgroup limits, the bwrap fs Policy (handed to the jail
// stage via bwrap.EnvPolicy), and the agent argv+env to exec once the fence is up. The
// agent argv is this process's own os.Args, so the agent re-enters the same command and,
// seeing bwrap.EnvPolicy, proceeds into the jail stage.
func planFor(cfg Config) (fence.Plan, error) {
	system := cfg.System
	if len(system) == 0 {
		system = []string{"/"}
	}
	provision := bwrap.Provision{
		RoleCard:  bwrap.Bind{Src: cfg.RoleCard},
		State:     bwrap.Bind{Src: cfg.State},
		Workspace: bwrap.Bind{Src: cfg.Workspace},
		Home:      bwrap.Bind{Src: cfg.Home},
	}
	// DNS-over-TCP resolv.conf, so a udp:drop policy still resolves. Generated here and
	// bound read-only into the jail at /etc/resolv.conf.
	if len(cfg.Nameservers) > 0 {
		rc, err := netpol.ResolvConf(cfg.Nameservers)
		if err != nil {
			return fence.Plan{}, err
		}
		// A deterministic per-agent path (not os.CreateTemp): the file is bound read-only
		// into the jail and cannot be unlinked before the exec chain, so a fresh temp file
		// every launch would accumulate. Writing the same path per agent overwrites in place.
		path := filepath.Join(os.TempDir(), "plexus-resolv-"+cfg.AgentID+".conf")
		if err := os.WriteFile(path, []byte(rc), 0o644); err != nil {
			return fence.Plan{}, fmt.Errorf("sandbox: write resolv.conf: %w", err)
		}
		provision.ResolvConf = bwrap.Bind{Src: path}
	}
	policyJSON, err := json.Marshal(bwrap.Policy{
		System:    system,
		Mask:      cfg.Mask,
		Clearenv:  cfg.Clearenv,
		Provision: provision,
		Uid:       cfg.UID,
		Gid:       cfg.GID,
	})
	if err != nil {
		return fence.Plan{}, fmt.Errorf("sandbox: marshal policy: %w", err)
	}

	// The relay port carves the proxy's own upstream out of the fence (no TPROXY loop).
	relayPort := 0
	if _, p, err := net.SplitHostPort(cfg.Relay); err == nil {
		relayPort, _ = strconv.Atoi(p)
	}

	// The agent's env is this stage's env MINUS the fence-stage marker (so the agent is
	// not mistaken for another fence stage) PLUS the egress policy + the bwrap Policy that
	// drives the jail stage.
	// Strip EVERY handover var from the ambient env before re-setting the ones we mean,
	// so a stale/ambient copy can't shadow (os.Getenv returns the first match).
	env := append(strippedEnv(envStage, EnvTicket, bwrap.EnvPolicy,
		egress.EnvNetTCP, egress.EnvNetUDP, egress.EnvRelay, egress.EnvTCPFD, egress.EnvUDPFD),
		egress.EnvNetTCP+"="+cfg.NetTCP,
		egress.EnvNetUDP+"="+cfg.NetUDP,
		bwrap.EnvPolicy+"="+string(policyJSON),
	)
	if cfg.Relay != "" {
		env = append(env, egress.EnvRelay+"="+cfg.Relay)
	}

	return fence.Plan{
		AgentID: cfg.AgentID,
		Net:     netpol.NetPolicy{TCP: netpol.ParseAction(cfg.NetTCP), UDP: netpol.ParseAction(cfg.NetUDP)},
		NFT: netpol.Params{
			CP: cfg.CP, BusPort: cfg.BusPort, RelayPort: relayPort, EgressPort: cfg.EgressPort,
			Mark: cfg.Mark, Table: cfg.Table, MaxConns: cfg.MaxConns,
		},
		Limits: fence.Limits{MemoryMax: cfg.MemMax, PidsMax: cfg.PidsMax},
		Agent:  fence.Cmd{Argv: os.Args, Env: env},
	}, nil
}

// strippedEnv returns os.Environ() with the named variables removed.
func strippedEnv(keys ...string) []string {
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && drop[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
