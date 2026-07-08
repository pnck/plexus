//go:build !linux

package sandbox

import (
	"fmt"
	"runtime"
)

// The sandbox is only wired for the linux/userns+bwrap backend. Other backends (e.g.
// macOS seatbelt) plug in behind the same Policy translation layer as future work; until
// one exists for this platform, --sandbox refuses cleanly at launch rather than
// half-establishing a sandbox.

func provider() (Provider, error) { return nil, unimplemented() }

func launchOrDegrade(Config) error { return unimplemented() }

func buildFence(Config) error { return unimplemented() }

func unimplemented() error {
	return fmt.Errorf("--sandbox is not implemented on %s yet: only the linux/userns+bwrap backend is "+
		"wired; other backends plug in behind the same Policy translation layer (future work)", runtime.GOOS)
}
