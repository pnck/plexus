//go:build linux

package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cgroup2 "github.com/containerd/cgroups/v3/cgroup2"
	"golang.org/x/sys/unix"
)

const mountpoint = "/sys/fs/cgroup"

// selfCgroupPath returns this process's cgroup v2 path relative to the mountpoint
// (from /proc/self/cgroup, a single "0::/path" line). The delegated writable
// subtree, if any, lives under here.
func selfCgroupPath() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "/"
	}
	line := strings.TrimSpace(string(data))
	if i := strings.LastIndex(line, "::"); i >= 0 && line[i+2:] != "" {
		return line[i+2:]
	}
	return "/"
}

// Available reports whether a writable, controller-delegated cgroup v2 subtree is
// usable here: cgroup2 must be mounted AND we must be able to create a child cgroup
// under our own cgroup dir. It is false in a plain unprivileged container (cgroup2
// mounted read-only) and where cgroup v1 is in use.
func Available() bool {
	var st unix.Statfs_t
	if err := unix.Statfs(mountpoint, &st); err != nil || st.Type != unix.CGROUP2_SUPER_MAGIC {
		return false
	}
	probe := filepath.Join(mountpoint, selfCgroupPath(), "plexus-probe")
	if err := os.Mkdir(probe, 0o755); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// Apply creates a per-agent cgroup v2 (named under this process's own cgroup), sets
// the limits, and moves the current process into it so children inherit it across
// exec. It returns a cleanup func that removes the cgroup, or ErrUnavailable when a
// delegated cgroup v2 subtree is not available (caller then relies on the rlimit
// floor). Integration-tested under a delegated/privileged environment; here (an
// unprivileged container) it returns ErrUnavailable.
func Apply(name string, l Limits) (func() error, error) {
	if !Available() {
		return nil, ErrUnavailable
	}
	res := &cgroup2.Resources{}
	if l.MemoryMax > 0 {
		m := l.MemoryMax
		res.Memory = &cgroup2.Memory{Max: &m}
	}
	if l.PidsMax > 0 {
		res.Pids = &cgroup2.Pids{Max: l.PidsMax}
	}
	group := filepath.Join(selfCgroupPath(), name)
	mgr, err := cgroup2.NewManager(mountpoint, group, res)
	if err != nil {
		return nil, fmt.Errorf("cgroup: create %q: %w", group, err)
	}
	if err := mgr.AddProc(uint64(os.Getpid())); err != nil {
		_ = mgr.Delete()
		return nil, fmt.Errorf("cgroup: add proc: %w", err)
	}
	return mgr.Delete, nil
}
