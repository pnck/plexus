package sandbox

import "plexus/sandbox/seccomp"

// Confinement is the Phase-2 restriction a sandboxed agent applies to ITSELF after
// bwrap has built its namespaces and before it runs any untrusted work (flow doc
// §4): lowered resource ceilings and a seccomp filter. Both are unprivileged and
// irreversible — the agent can only shrink its own surface, never widen it.
type Confinement struct {
	Rlimits Rlimits
	Seccomp seccomp.Profile
}

// DefaultConfinement is the baseline every sandboxed agent applies when no per-agent
// policy is injected: a generous rlimit floor (fd + fork-bomb guards) and the
// default seccomp denylist of escape / kernel-attack syscalls. A per-agent policy
// from the E5 catalog tightens the limits (memory / file-size caps) later; the
// values here are deliberately loose so they never break ordinary dev work.
func DefaultConfinement() Confinement {
	return Confinement{
		Rlimits: Rlimits{NOFILE: 8192, NPROC: 2048},
		Seccomp: seccomp.DefaultProfile(),
	}
}
