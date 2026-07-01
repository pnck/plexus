//go:build linux

package seccomp

import "testing"

// The default profile must assemble to BPF on the build arch (catches a syscall
// name that does not resolve on amd64/arm64).
func TestDefaultProfileAssembles(t *testing.T) {
	if err := Validate(DefaultProfile()); err != nil {
		t.Fatalf("DefaultProfile does not assemble: %v", err)
	}
}

func TestProfileValidation(t *testing.T) {
	// denylist (default-allow, listed denied) and allowlist (default-deny, listed
	// allowed) both assemble.
	if err := Validate(Profile{DefaultAllow: true, Syscalls: []string{"ptrace", "mount"}}); err != nil {
		t.Fatalf("denylist: %v", err)
	}
	if err := Validate(Profile{DefaultAllow: false, Syscalls: []string{"read", "write", "exit_group"}}); err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	// an unknown syscall name is rejected.
	if err := Validate(Profile{DefaultAllow: true, Syscalls: []string{"definitely_not_a_syscall"}}); err == nil {
		t.Fatal("expected error for unknown syscall name")
	}
}
