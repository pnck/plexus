package bwrap

import (
	"fmt"
	"strings"
)

// This file is the E4.2 sandbox translation layer, designed around plexus RUNTIME
// SEMANTICS rather than as a 1:1 mirror of bwrap's flags. A Policy says what a
// sandboxed plexus agent's world IS — its read-only base image, the paths injected
// into it, what's hidden, and its environment. Translate lowers that to the exact
// bwrap invocation, BAKING IN the isolation invariants every sandboxed agent
// shares: full namespace isolation, an inherited network namespace, /dev+/proc+
// /tmp scaffolding, cap-drop ALL, and die-with-parent. Those are not knobs — plexus
// never varies them, so they are not fields, and the impossible bwrap combinations
// they would allow simply do not exist here.
//
// Network egress is NOT a bwrap concern: it is netpol.NetPolicy (redirect/reject/
// drop per protocol), lowered to nft/tproxy by Setup, and carried alongside this
// Policy in sandbox.Environment. bwrap only ever inherits the prepared netns
// (--share-net), which is one of the baked invariants below.

// Bind is a host->sandbox mount: the host Src path is made visible at Dest inside
// the sandbox.
type Bind struct{ Src, Dest string }

// EnvVar is one environment variable granted into the sandbox.
type EnvVar struct{ Key, Value string }

// Policy is plexus's semantic description of one sandboxed agent's filesystem +
// environment — only what VARIES per agent. The isolation invariants are baked into
// Translate, not exposed here.
type Policy struct {
	// System is the read-only base rootfs the agent sees (ro-bind) — the curated
	// image that is its world. Default: the whole host "/"; a per-agent production
	// policy (E4.6) narrows it to a minimal subset.
	System []string

	// Provision is what the launcher injects (E4.4): the role card (read-only, so an
	// agent cannot rewrite its own authority) and the writable state / workspace /
	// HOME. It also sets the working directory (the workspace) and HOME.
	Provision Provision

	// Mask hides sensitive host paths (e.g. /prod, host secrets, other agents' trees)
	// behind an empty tmpfs, so the agent can neither see nor reach them.
	Mask []string

	// Clearenv seals the environment (drop everything inherited); Env is then the
	// only environment the agent gets — the secret face. A permissive/dev policy
	// leaves it false to inherit the host env.
	Clearenv bool
	Env      []EnvVar
}

// DefaultPolicy is the sensible default for running a single agent (chat/dev) in
// sandbox mode — which is opt-in; the normal path is un-sandboxed. It shows the
// whole host filesystem read-only and inherits the environment (dev convenience). A
// per-agent production policy (E4.6) narrows System, masks secrets, and seals env.
func DefaultPolicy() Policy {
	return Policy{System: []string{"/"}}
}

// Translate lowers a Policy into bwrap CLI arguments, EXCLUDING the bwrap binary
// path, the ticket bind, and the trailing "-- <argv>" (those are the sandbox
// mechanism's job in Enter, not isolation policy). It bakes in the invariants every
// sandboxed agent shares; argument order honors bwrap semantics (broad mounts, then
// overlays, then namespaces, then env, then confinement/lifecycle).
func Translate(p Policy) []string {
	var a []string
	// The read-only base rootfs — the agent's world.
	for _, sys := range p.System {
		a = append(a, "--ro-bind", sys, sys)
	}
	// Standard sandbox scaffolding (invariant): a minimal /dev, the agent's own
	// /proc, and an ephemeral /tmp.
	a = append(a, "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp")
	// Injected cognition (E4.4): role card ro, state/workspace/home rw, chdir, HOME.
	a = append(a, p.Provision.args()...)
	// Hide sensitive host paths behind an empty tmpfs.
	for _, m := range p.Mask {
		a = append(a, "--tmpfs", m)
	}
	// Namespaces (invariant): isolate pid/ipc/uts/cgroup/user; KEEP (inherit) the
	// network namespace Setup prepared — never a fresh, routeless one.
	a = append(a, "--unshare-all", "--share-net")
	// Environment (secret face).
	if p.Clearenv {
		a = append(a, "--clearenv")
	}
	for _, e := range p.Env {
		a = append(a, "--setenv", e.Key, e.Value)
	}
	// Confinement + lifecycle (invariant): the agent is unprivileged and must never
	// outlive its launcher.
	a = append(a, "--cap-drop", "ALL", "--die-with-parent")
	return a
}

// Describe renders the LLM-facing summary of the filesystem + namespace
// confinement — the "what can I touch, what's hidden" an agent needs up front. It
// feeds the sandbox environment-state L1 frame (composed with the network limits by
// sandbox.Environment.Describe(), E4.5). The bwrap mechanism is never surfaced.
func (p Policy) Describe() string {
	var b strings.Builder
	if w := p.Provision.writable(); w != "" {
		fmt.Fprintf(&b, "Writable paths: %s.\n", w)
	}
	if ro := p.readOnly(); ro != "" {
		fmt.Fprintf(&b, "Read-only paths (you cannot modify these): %s.\n", ro)
	}
	fmt.Fprintf(&b, "Ephemeral in-memory paths (cleared on exit): %s.\n",
		strings.Join(append([]string{"/tmp"}, p.Mask...), ", "))
	if wd := p.Provision.workdir(); wd != "" {
		fmt.Fprintf(&b, "Working directory: %s.\n", wd)
	}
	b.WriteString("You run in isolated pid/ipc/user namespaces; host processes and other agents are not visible.")
	return b.String()
}

// readOnly lists the read-only sandbox paths — the system base plus the role card.
func (p Policy) readOnly() string {
	ro := append([]string{}, p.System...)
	if rc := p.Provision.roleCardDest(); rc != "" {
		ro = append(ro, rc)
	}
	return strings.Join(ro, ", ")
}
