// Package cgroup applies cgroup v2 resource limits to the agent process (E4.3,
// ambient face) via containerd/cgroups (pure Go, no CGO). It is the STRONG layer
// above the universal rlimit floor (sandbox.ApplyRlimits) — PidsMax is a true
// per-cgroup fork-bomb cap and MemoryMax a real RSS/OOM cap. It is only usable
// where a writable, delegated cgroup v2 subtree exists (root / systemd
// Delegate=yes / privileged container); a plain unprivileged container mounts
// cgroup2 read-only, so callers fall back to rlimit. See docs/implement-design §5.6.
package cgroup

import "errors"

// ErrUnavailable is returned by Apply when no writable, delegated cgroup v2 subtree
// is available. Callers fall back to the rlimit floor.
var ErrUnavailable = errors.New("cgroup: no writable delegated cgroup v2 subtree")

// Limits are cgroup v2 resource ceilings. A zero field means "unset".
type Limits struct {
	MemoryMax int64 // memory.max bytes (real RSS + OOM-kill)
	PidsMax   int64 // pids.max (true fork-bomb cap)
}
