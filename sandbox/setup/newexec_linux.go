//go:build linux

package setup

// NewExecutor returns the real privileged Phase-0 executor.
func NewExecutor() (Executor, error) { return NewOSExecutor(), nil }
