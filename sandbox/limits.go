package sandbox

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
