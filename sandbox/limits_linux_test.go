//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

// ApplyRlimits changes process-global state (and lowering the hard limit is
// irreversible), so it runs in a child process to avoid affecting the test binary.
func TestApplyRlimits(t *testing.T) {
	if os.Getenv("PLEXUS_RLIMIT_CHILD") == "1" {
		// Child: apply a low NOFILE ceiling and verify it took.
		if err := ApplyRlimits(Rlimits{NOFILE: 128}); err != nil {
			t.Fatalf("child ApplyRlimits: %v", err)
		}
		var r unix.Rlimit
		if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &r); err != nil {
			t.Fatalf("child Getrlimit: %v", err)
		}
		if r.Cur != 128 || r.Max != 128 {
			t.Fatalf("child NOFILE = {cur:%d max:%d}, want 128/128", r.Cur, r.Max)
		}
		// A zero field must leave the limit unchanged.
		if err := ApplyRlimits(Rlimits{}); err != nil {
			t.Fatalf("child ApplyRlimits(zero): %v", err)
		}
		if unix.Getrlimit(unix.RLIMIT_NOFILE, &r); r.Cur != 128 {
			t.Fatalf("zero Rlimits changed NOFILE to %d", r.Cur)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestApplyRlimits$", "-test.v")
	cmd.Env = append(os.Environ(), "PLEXUS_RLIMIT_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
}
