//go:build linux

package sandbox

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// ApplyRlimits lowers the current process's resource ceilings. Children inherit
// them across exec, so calling this in the HOST phase before the sandboxed
// re-exec covers the whole agent process tree. Both the soft and hard limit are
// set to the value so the agent cannot raise them back (a hardened role also
// drops CAP_SYS_RESOURCE). A zero field is skipped.
func ApplyRlimits(l Rlimits) error {
	for _, e := range []struct {
		res  int
		val  uint64
		name string
	}{
		{unix.RLIMIT_NOFILE, l.NOFILE, "NOFILE"},
		{unix.RLIMIT_NPROC, l.NPROC, "NPROC"},
		{unix.RLIMIT_FSIZE, l.FSIZE, "FSIZE"},
		{unix.RLIMIT_AS, l.AS, "AS"},
	} {
		if e.val == 0 {
			continue
		}
		if err := unix.Setrlimit(e.res, &unix.Rlimit{Cur: e.val, Max: e.val}); err != nil {
			return fmt.Errorf("setrlimit %s=%d: %w", e.name, e.val, err)
		}
	}
	return nil
}
