//go:build linux

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"

	"plexus/sandbox/bwrap"
	"plexus/sandbox/egress"
	"plexus/sandbox/fence"
	"plexus/sandbox/netpol"
)

// provider returns the linux filesystem-jail backend: bwrap, configured from the Policy
// handed down via bwrap.EnvPolicy (or the permissive default when none was set).
func provider() (Provider, error) { return bwrap.ProviderFromEnv() }

// launchOrDegrade is the fresh-launch decision (§ implement-design 5.6.9, two-tier):
//   - CAP_NET_ADMIN present → launch the full network fence (netns + veth + nft/TPROXY).
//   - absent + RequireNetFence → clean error telling the operator to grant it.
//   - absent → degrade: no network fence, the agent runs on the host network (no
//     per-agent isolation/audit); warn, and go straight to the fs/seccomp jail.
//
// The core sandbox (fs / user / pid / ipc ns + seccomp + rlimit) is zero host cap and
// always applies; only the network audit tier is gated on CAP_NET_ADMIN.
func launchOrDegrade(cfg Config) error {
	if hasNetAdmin() {
		return launch(cfg)
	}
	if cfg.RequireNetFence {
		return fmt.Errorf("sandbox: --require-net-fence set but CAP_NET_ADMIN is unavailable — grant it via " +
			"root / a privileged container / `--cap-add=NET_ADMIN` / systemd AmbientCapabilities (NOT `setcap`: a " +
			"file-capability binary can't create the unprivileged userns the netns needs), or drop --require-net-fence")
	}
	slog.Warn("network fence disabled: CAP_NET_ADMIN unavailable — the agent runs on the HOST network with " +
		"NO per-agent network isolation or egress audit; grant CAP_NET_ADMIN to enable the fence")
	return degradeToJail(cfg)
}

// launch (full path, CAP_NET_ADMIN held) forks THIS command — same argv — into a fresh
// user+network namespace (CLONE_NEWUSER|CLONE_NEWNET; the userns creates the netns with
// no CAP_SYS_ADMIN), then, as the host-netns launcher, builds a veth into the child's
// netns (routed to the CP) and signals the child that the network is ready. The child
// proceeds to the fence stage; the launcher stays a thin supervisor and propagates the
// child's exit. Returns only on a spawn/veth error.
func launch(cfg Config) error {
	// Pipe: the launcher writes nothing and closes the write end once the veth is in
	// place; the child (fence stage) blocks reading the read end until that EOF.
	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("sandbox: pipe: %w", err)
	}

	cmd := exec.Command("/proc/self/exe", os.Args[1:]...)
	cmd.Args[0] = os.Args[0]
	cmd.Env = append(os.Environ(), envStage+"="+stageFenced, envVethReadyFD+"=3")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{r} // → child fd 3

	// Map the userns root to the OWNER of this binary (fallback: the caller's uid/gid), so
	// the re-exec'd self-binary — and the directories on its path — stay owner-accessible
	// inside the userns. Under root a lone 0->0 map leaves a binary owned by an unprivileged
	// user (and e.g. a 0750 /home/<user> on its path) unreachable to the confined agent,
	// which then can't be re-exec'd (EACCES on stat / ENOENT on execvp).
	hostUID, hostGID := os.Getuid(), os.Getgid()
	if fi, err := os.Stat("/proc/self/exe"); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			hostUID, hostGID = int(st.Uid), int(st.Gid)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: hostUID, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: hostGID, Size: 1}},
		GidMappingsEnableSetgroups: false, // an unprivileged gid map requires setgroups=deny
	}
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return fmt.Errorf("sandbox: entering an unprivileged user+network namespace failed (%w) — "+
			"`--sandbox` needs unprivileged user namespaces enabled (`sysctl kernel.unprivileged_userns_clone` / "+
			"`user.max_user_namespaces`). NOTE: if you granted CAP_NET_ADMIN via `setcap`, that IS the cause — a "+
			"file-capability binary can't create an unprivileged userns; grant the cap via root / --cap-add=NET_ADMIN", err)
	}
	_ = r.Close() // the launcher keeps only the write end

	// Launcher (holds CAP_NET_ADMIN): build the veth into the child's netns, then signal.
	if err := setupHostVeth(cfg, cmd.Process.Pid); err != nil {
		_ = w.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return fmt.Errorf("sandbox: build veth: %w", err)
	}
	_ = w.Close() // veth ready → unblock the child

	forwardSignals(cmd.Process)
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
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

// setupHostVeth creates the veth pair in the host netns, moves the peer into the child's
// netns (referenced by its /proc/<pid>/ns/net fd — no setns, no CAP_SYS_ADMIN), and
// gives the host end HostCIDR + up so it is the agent's gateway to the control plane. It
// needs CAP_NET_ADMIN (the network-fence prerequisite), held by this launcher process.
func setupHostVeth(cfg Config, childPid int) error {
	runtime.LockOSThread()

	nsf, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", childPid))
	if err != nil {
		return fmt.Errorf("open child netns: %w", err)
	}
	defer nsf.Close()

	la := netlink.NewLinkAttrs()
	la.Name = cfg.VethHost
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: la, PeerName: cfg.VethPeer}); err != nil {
		return fmt.Errorf("create veth %s<->%s: %w", cfg.VethHost, cfg.VethPeer, err)
	}
	peer, err := netlink.LinkByName(cfg.VethPeer)
	if err != nil {
		return fmt.Errorf("find peer %s: %w", cfg.VethPeer, err)
	}
	if err := netlink.LinkSetNsFd(peer, int(nsf.Fd())); err != nil {
		return fmt.Errorf("move %s into child netns: %w", cfg.VethPeer, err)
	}
	host, err := netlink.LinkByName(cfg.VethHost)
	if err != nil {
		return fmt.Errorf("find host veth %s: %w", cfg.VethHost, err)
	}
	addr, err := netlink.ParseAddr(cfg.HostCIDR)
	if err != nil {
		return fmt.Errorf("parse host cidr %s: %w", cfg.HostCIDR, err)
	}
	if err := netlink.AddrAdd(host, addr); err != nil {
		return fmt.Errorf("host veth addr %s: %w", cfg.HostCIDR, err)
	}
	if err := netlink.LinkSetUp(host); err != nil {
		return fmt.Errorf("host veth up: %w", err)
	}
	return nil
}

// forwardSignals makes the host-netns supervisor relay service-teardown signals to the
// sandbox child. SIGINT is deliberately ignored here (the child shares the terminal's
// process group, so an interactive Ctrl-C already reaches it directly — forwarding would
// double-deliver); SIGTERM/SIGHUP/SIGQUIT ARE forwarded so the child tears down with it.
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

// buildFence (fence stage) runs inside the fresh user+network namespace: it blocks until
// the launcher signals the veth is ready, then builds the network fence (agent-side veth
// + nft + TPROXY + cgroup) and execs the agent onward into the jail stage. Returns only
// on error. The kernel work stays on one locked OS thread.
func buildFence(cfg Config) error {
	runtime.LockOSThread()
	waitVethReady()
	plan, err := planFor(cfg)
	if err != nil {
		return err
	}
	return fence.Build(plan, fence.NewOSBuilder())
}

// waitVethReady blocks on the inherited pipe until the launcher closes its write end,
// which means the veth peer is now present in this netns.
func waitVethReady() {
	fd := os.Getenv(envVethReadyFD)
	if fd == "" {
		return
	}
	n, err := strconv.Atoi(fd)
	if err != nil {
		return
	}
	p := os.NewFile(uintptr(n), "veth-ready")
	if p == nil {
		return
	}
	var b [1]byte
	_, _ = p.Read(b[:]) // returns on EOF once the launcher closes the write end
	_ = p.Close()
}

// degradeToJail is the no-CAP_NET_ADMIN path: no netns, no fence. It assembles the bwrap
// fs Policy and enters the jail directly (bwrap --share-net inherits the host netns), so
// the agent gets fs/user/pid/ipc isolation + seccomp/rlimit but the host network.
func degradeToJail(cfg Config) error {
	policyJSON, err := bwrapPolicyJSON(cfg)
	if err != nil {
		return err
	}
	if err := os.Setenv(bwrap.EnvPolicy, policyJSON); err != nil {
		return fmt.Errorf("sandbox: set policy: %w", err)
	}
	return jail()
}

// bwrapPolicyJSON assembles the per-agent bwrap fs Policy (system rootfs, provision
// binds, masks, sealed env, uid/gid) as JSON — shared by the fenced path (planFor) and
// the degraded path (degradeToJail). It also generates the DNS-over-TCP resolv.conf.
func bwrapPolicyJSON(cfg Config) (string, error) {
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
	if len(cfg.Nameservers) > 0 {
		rc, err := netpol.ResolvConf(cfg.Nameservers)
		if err != nil {
			return "", err
		}
		// Deterministic per-agent path (bound read-only into the jail; can't be unlinked
		// before exec, so a fresh temp per launch would accumulate — overwrite in place).
		path := filepath.Join(os.TempDir(), "plexus-resolv-"+cfg.AgentID+".conf")
		if err := os.WriteFile(path, []byte(rc), 0o644); err != nil {
			return "", fmt.Errorf("sandbox: write resolv.conf: %w", err)
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
		return "", fmt.Errorf("sandbox: marshal policy: %w", err)
	}
	return string(policyJSON), nil
}

// planFor assembles the fence Plan from the (defaulted) Config: the agent-side veth, the
// egress fence (deny-all by default), the cgroup limits, the bwrap Policy (handed to the
// jail stage via bwrap.EnvPolicy), and the agent argv+env. The agent argv is this
// process's own os.Args, so it re-enters the same command and, seeing bwrap.EnvPolicy,
// proceeds into the jail stage.
func planFor(cfg Config) (fence.Plan, error) {
	policyJSON, err := bwrapPolicyJSON(cfg)
	if err != nil {
		return fence.Plan{}, err
	}

	// The relay port carves the proxy's own upstream out of the fence (no TPROXY loop).
	relayPort := 0
	if _, p, err := net.SplitHostPort(cfg.Relay); err == nil {
		relayPort, _ = strconv.Atoi(p)
	}

	// The agent env is this stage's env MINUS every handover var (so an ambient copy can't
	// shadow) PLUS the egress policy + the bwrap Policy that drives the jail stage.
	env := append(strippedEnv(envStage, envVethReadyFD, EnvTicket, bwrap.EnvPolicy,
		egress.EnvNetTCP, egress.EnvNetUDP, egress.EnvRelay, egress.EnvTCPFD, egress.EnvUDPFD),
		egress.EnvNetTCP+"="+cfg.NetTCP,
		egress.EnvNetUDP+"="+cfg.NetUDP,
		bwrap.EnvPolicy+"="+policyJSON,
	)
	if cfg.Relay != "" {
		env = append(env, egress.EnvRelay+"="+cfg.Relay)
	}

	return fence.Plan{
		AgentID:   cfg.AgentID,
		VethPeer:  cfg.VethPeer,
		AgentCIDR: cfg.AgentCIDR,
		Gateway:   cfg.Gateway,
		Net:       netpol.NetPolicy{TCP: netpol.ParseAction(cfg.NetTCP), UDP: netpol.ParseAction(cfg.NetUDP)},
		NFT: netpol.Params{
			CP: cfg.CP, BusPort: cfg.BusPort, RelayPort: relayPort, EgressPort: cfg.EgressPort,
			Mark: cfg.Mark, Table: cfg.Table, MaxConns: cfg.MaxConns,
		},
		Limits: fence.Limits{MemoryMax: cfg.MemMax, PidsMax: cfg.PidsMax},
		Agent:  fence.Cmd{Argv: agentArgv(), Env: env},
	}, nil
}

// agentArgv is os.Args with argv[0] resolved to the absolute self-path, so the fence->jail
// re-exec and bwrap's exec of the agent don't depend on cwd (the launch argv[0] may be
// relative, e.g. `dl/plexus-linux-amd64`).
func agentArgv() []string {
	argv := append([]string(nil), os.Args...)
	if exe, err := os.Executable(); err == nil {
		argv[0] = exe
	}
	return argv
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
