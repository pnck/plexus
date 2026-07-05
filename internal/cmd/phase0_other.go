//go:build !linux

package cmd

import (
	"fmt"
	"runtime"
)

// runPhase0 is unimplemented off the supported sandbox platform. plexus keeps a clean
// Policy translation-layer boundary — bwrap is the only wired backend (linux), and
// other backends (e.g. macOS seatbelt) plug in behind the same Policy/Provider
// abstraction as future work. Until one exists for this platform, `--sandbox` refuses
// up front rather than half-establishing a sandbox.
func (c *sandboxConfig) runPhase0() error {
	return fmt.Errorf("--sandbox is not implemented on %s yet: only the linux/bwrap backend is wired; "+
		"other backends plug in behind the same Policy translation layer (future work)", runtime.GOOS)
}
