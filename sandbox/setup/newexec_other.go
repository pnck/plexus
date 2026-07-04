//go:build !linux

package setup

import "fmt"

// NewExecutor is unavailable off Linux — the privileged sandbox only runs there.
func NewExecutor() (Executor, error) {
	return nil, fmt.Errorf("setup: the real executor is only supported on linux")
}
