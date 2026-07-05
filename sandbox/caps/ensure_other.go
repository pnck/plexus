//go:build !linux

package caps

import "fmt"

// Missing treats every wanted capability as unavailable off Linux (caps are a Linux
// concept), so a preflight fails closed.
func Missing(want Set) Set { return want }

// Ensure is unavailable off Linux — capabilities are a Linux concept and the sandbox
// only runs there. It fails closed when anything is actually required.
func Ensure(want Set) error {
	if want.Empty() {
		return nil
	}
	return fmt.Errorf("caps: raising capabilities is only supported on linux (need %s)", want.Describe())
}
