// Package sandbox establishes an agent's isolation as a chain of process re-execs, each
// adding one layer of confinement, selected by the handover env the previous stage set:
//
//	launch   (host netns)          fork this command into a fresh USER+NETWORK namespace
//	                               (CLONE_NEWUSER|CLONE_NEWNET); the launcher stays a thin
//	                               host-netns supervisor and, holding CAP_NET_ADMIN, builds
//	                               the veth into the child's netns (the network tier).
//	fence    (in the new netns)    configure the agent-side veth + nft/TPROXY egress fence
//	                               + resource cgroup with the in-userns CAP_NET_ADMIN, then
//	                               exec onward.
//	jail     (bwrap)               build the fs/mount/user/pid/ipc isolation (--cap-drop
//	                               ALL, so the agent runs zero-cap), then exec.
//	confine  (in the jail)         drop rlimits + load seccomp, then return to run.
//
// Two tiers, gated on CAP_NET_ADMIN (§ implement-design 5.6.9):
//   - the CORE sandbox (fs/user/pid/ipc ns + seccomp + rlimit) needs only unprivileged
//     user namespaces — the same thing bwrap itself needs — and zero host capability;
//   - the NETWORK fence (per-agent netns + veth to the CP + nft/TPROXY audit) needs
//     CAP_NET_ADMIN, held by the launcher solely to BUILD the veth and dropped before the
//     agent runs. Without it the fence degrades (host network + warn); the agent is
//     confined either way. No CAP_SYS_ADMIN / setns / named netns is ever used.
//
// On platforms without a wired backend, Enter refuses cleanly at launch.
package sandbox

import (
	"fmt"
	"log/slog"
	"os"

	"plexus/sandbox/bwrap"
)

// EnvTicket names the one-time handover ticket path. Its presence in the environment is
// how a process knows it is already INSIDE the jail (the confine stage) rather than
// building toward it — the entry state machine keys the last stage on it.
const EnvTicket = "PLEXUS_SANDBOX_TICKET"

// envStage marks the in-namespace fence stage across the launch→fence re-exec. The jail
// and confine stages are keyed on bwrap.EnvPolicy / EnvTicket respectively, so only the
// fence stage needs its own marker.
const (
	envStage    = "PLEXUS_SANDBOX_STAGE"
	stageFenced = "fenced"
	// envVethReadyFD names the inherited pipe fd the fence stage blocks on until the
	// launcher has built the veth into the netns (it closes the write end to signal).
	envVethReadyFD = "PLEXUS_VETH_READY_FD"
)

// Provider is a sandbox backend: it builds the filesystem/namespace jail from the
// assembled Policy and execs the agent into it. bwrap is the only wired backend
// (Linux); other backends (e.g. macOS seatbelt) plug in behind this same interface,
// translating the same backend-neutral Policy. provider() returns the platform's.
type Provider interface {
	Name() string
	Enter(ticketPath string, extraArgs []string) error
}

// Enter establishes the sandbox for the current command and returns once the agent is
// confined and may run. It drives the launch→fence→jail→confine chain across re-execs;
// the host-side stages exec away and never return here, so a nil return means "you are
// inside the finished sandbox — proceed". Call it only when --sandbox is requested.
func Enter(cfg Config) error {
	// Give each agent its own veth/CIDR (deterministic in AgentID) so concurrent sandboxes
	// don't collide; both the launcher and the re-exec'd fence stage derive the same values.
	cfg.deriveAgentNet()
	switch {
	case os.Getenv(EnvTicket) != "":
		return confine()
	case os.Getenv(bwrap.EnvPolicy) != "":
		return jail()
	case os.Getenv(envStage) == stageFenced:
		return buildFence(cfg)
	default:
		return launchOrDegrade(cfg)
	}
}

// jail (stage 3) generates the one-time ticket and execs the filesystem-jail backend
// (bwrap), which rebuilds the process inside mount/user/pid/ipc namespaces and re-execs
// the agent carrying the ticket. It returns only on error.
func jail() error {
	p, err := provider()
	if err != nil {
		return err
	}
	ticketPath, err := GenerateTicket()
	if err != nil {
		return fmt.Errorf("generate sandbox ticket: %w", err)
	}
	if err := os.Setenv(EnvTicket, ticketPath); err != nil {
		return fmt.Errorf("set sandbox ticket: %w", err)
	}
	slog.Info("entering filesystem jail", "backend", p.Name())
	return p.Enter(ticketPath, nil)
}

// confine (stage 4) is the innermost stage: it verifies+consumes the ticket (proving a
// deterministic entry, not a stray re-exec), then applies the unprivileged, irreversible
// self-confinement — lower rlimits, then the seccomp filter — before returning so the
// caller runs the agent. Both are the last things before any untrusted work.
func confine() error {
	if err := VerifyAndConsumeTicket(os.Getenv(EnvTicket)); err != nil {
		return fmt.Errorf("sandbox ticket: %w", err)
	}
	if err := confineSelf(DefaultConfinement()); err != nil {
		return fmt.Errorf("sandbox confinement: %w", err)
	}
	slog.Info("sandbox active — agent confined")
	return nil
}
