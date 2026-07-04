//go:build !linux

package sandbox

import "fmt"

// confineSelf is unreachable off Linux — the sandbox phase is only ever entered
// after a Linux bwrap re-exec — but it must exist for the build. Fail closed.
func confineSelf(Confinement) error {
	return fmt.Errorf("sandbox: phase-2 confinement is only supported on linux")
}
