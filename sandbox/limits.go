package sandbox

import (
	"fmt"
	"strings"
)

// Rlimits are per-process resource ceilings applied via setrlimit before the
// sandboxed exec (E4.3, ambient face). They are the UNIVERSAL FLOOR: setrlimit
// needs no privilege or cgroup delegation, so it works everywhere — including a
// plain unprivileged container where cgroup v2 delegation is unavailable. The
// cgroup v2 layer (sandbox/cgroup) adds stronger, per-cgroup limits ON TOP when
// available (pids.max real fork-bomb cap, memory.max real OOM). A zero field
// means "leave that limit unchanged". See tracking/E4-sandbox-isolation.md.
type Rlimits struct {
	NOFILE uint64 // RLIMIT_NOFILE: max open fds
	NPROC  uint64 // RLIMIT_NPROC: max processes for the uid (weak, per-uid fork-bomb floor)
	FSIZE  uint64 // RLIMIT_FSIZE: max single-file size in bytes (per file, not total disk)
	AS     uint64 // RLIMIT_AS: max virtual address space in bytes (coarse memory floor)
}

// Describe renders the LLM-facing resource ceilings for the env-state frame — the
// limits the agent should plan within (only the set, non-zero fields; a zero field
// means no explicit ceiling for that resource). It surfaces the limits, not the
// setrlimit/cgroup mechanism (sandbox.Environment.Describe(), E4.5).
func (r Rlimits) Describe() string {
	var parts []string
	if r.NPROC > 0 {
		parts = append(parts, fmt.Sprintf("up to %d processes", r.NPROC))
	}
	if r.AS > 0 {
		parts = append(parts, humanBytes(r.AS)+" address space")
	}
	if r.FSIZE > 0 {
		parts = append(parts, humanBytes(r.FSIZE)+" max file size")
	}
	if r.NOFILE > 0 {
		parts = append(parts, fmt.Sprintf("%d open files", r.NOFILE))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Resource limits: " + strings.Join(parts, ", ") + "."
}

// humanBytes renders a byte count with a binary unit (rounded down) for Describe.
func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%dGiB", n>>30)
	case n >= 1<<20:
		return fmt.Sprintf("%dMiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%dKiB", n>>10)
	default:
		return fmt.Sprintf("%dB", n)
	}
}
