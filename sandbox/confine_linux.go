//go:build linux

package sandbox

import (
	"fmt"

	"plexus/sandbox/seccomp"
)

// confineSelf applies the Phase-2 restrictions in order: rlimits first, then the
// seccomp filter LAST — seccomp must come after every other setup call (including
// setrlimit) so it never blocks them, and it is the final thing before the agent's
// cognitive loop begins (flow doc §4/§8). Both steps need no host privilege and are
// irreversible.
func confineSelf(c Confinement) error {
	if err := ApplyRlimits(c.Rlimits); err != nil {
		return fmt.Errorf("rlimits: %w", err)
	}
	if err := seccomp.Load(c.Seccomp); err != nil {
		return fmt.Errorf("seccomp: %w", err)
	}
	return nil
}
