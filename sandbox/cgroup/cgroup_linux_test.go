//go:build linux

package cgroup

import "testing"

// In an unprivileged container (this dev env: cgroup2 mounted read-only) Available
// is false and Apply returns ErrUnavailable, so the caller falls back to the rlimit
// floor. Where a delegated cgroup v2 subtree exists, the degrade path is not
// exercised and the real limits are integration-tested separately.
func TestApplyDegradesWhenUnavailable(t *testing.T) {
	if Available() {
		t.Skip("cgroup v2 delegation available here; degrade path not exercised")
	}
	if _, err := Apply("plexus-test", Limits{PidsMax: 64, MemoryMax: 1 << 30}); err != ErrUnavailable {
		t.Fatalf("Apply on unavailable cgroup = %v, want ErrUnavailable", err)
	}
}
