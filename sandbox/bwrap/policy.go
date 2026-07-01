package bwrap

// This file is E4.2 — the sandbox translation-layer abstraction (formerly E3.5).
// It lowers a provider-neutral sandbox Policy (the "plexus flags") into concrete
// bwrap CLI arguments. The Policy vocabulary follows E4.1's three faces:
//
//   - confine   : what the agent must NOT reach (mounts, network, namespaces)
//   - provision : what the launcher injects read-only (role card, workspace, …)
//   - ambient   : global one-shot properties (die-with-parent, …)
//
// E4.2 ships the MECHANISM + a behavior-preserving DefaultPolicy (== E0's former
// hardcoded args). E4.3 fills the three faces with real per-agent values (seccomp,
// cgroup, bus-only netns, --role-card injection); E4.4 threads a per-agent Policy
// through EnterIfRequested. See tracking/E4-sandbox-isolation.md.
//
// The Policy fields are semantic (plexus-level), not bwrap-CLI-shaped, so a second
// sandbox provider could translate the same Policy differently. Kept in this
// package while there is one provider; promote to sandbox/ when a second lands.

// Bind is a host->sandbox mount: the host Src path is made visible at Dest inside
// the sandbox.
type Bind struct{ Src, Dest string }

// Policy describes the isolation an agent's effector-host runs under. Build it from
// DefaultPolicy (or a per-agent policy, E4.3); the zero value emits no arguments.
type Policy struct {
	// ── confine ──
	ROBinds    []Bind   // read-only mounts (--ro-bind); E0 default: {"/","/"} (whole rootfs)
	Binds      []Bind   // read-write mounts (--bind); provision writable paths go here
	Dev        []string // devtmpfs mounts (--dev)
	Proc       []string // proc mounts (--proc)
	Tmpfs      []string // tmpfs mounts (--tmpfs)
	UnshareAll bool     // --unshare-all (net/pid/ipc/uts/cgroup/user)
	// ShareNet re-exposes the network after UnshareAll. Net-off = UnshareAll &&
	// !ShareNet. Fine-grained "bus reachable, internet denied" is NOT a single flag
	// (net is non-binary, E4.1 §2) and lands in E4.3.
	ShareNet bool

	// ── ambient ──
	DieWithParent bool // --die-with-parent: never outlive the launcher (no orphan sandbox)
}

// DefaultPolicy reproduces E0's former hardcoded bwrap args EXACTLY, so routing
// Enter through Translate(DefaultPolicy()) is behavior-preserving. It is
// deliberately permissive (whole rootfs read-only, network shared, no seccomp/cap
// drop); E4.3 tightens it per agent.
func DefaultPolicy() Policy {
	return Policy{
		ROBinds:    []Bind{{Src: "/", Dest: "/"}},
		Dev:        []string{"/dev"},
		Proc:       []string{"/proc"},
		Tmpfs:      []string{"/tmp"},
		UnshareAll: true,
		ShareNet:   true,
	}
}

// Translate lowers a Policy into bwrap CLI arguments, EXCLUDING the bwrap binary
// path, the ticket bind, and the trailing "-- <argv>" — those are the sandbox
// mechanism's job in Enter, not isolation policy. Argument order is deterministic
// and honors bwrap semantics: mounts first, then namespace unshare/reshare, then
// writable binds, then ambient flags.
func Translate(p Policy) []string {
	var a []string
	for _, b := range p.ROBinds {
		a = append(a, "--ro-bind", b.Src, b.Dest)
	}
	for _, d := range p.Dev {
		a = append(a, "--dev", d)
	}
	for _, pr := range p.Proc {
		a = append(a, "--proc", pr)
	}
	for _, t := range p.Tmpfs {
		a = append(a, "--tmpfs", t)
	}
	if p.UnshareAll {
		a = append(a, "--unshare-all")
	}
	if p.ShareNet {
		a = append(a, "--share-net")
	}
	for _, b := range p.Binds {
		a = append(a, "--bind", b.Src, b.Dest)
	}
	if p.DieWithParent {
		a = append(a, "--die-with-parent")
	}
	return a
}
