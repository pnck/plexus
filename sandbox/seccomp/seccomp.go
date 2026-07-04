// Package seccomp builds and installs the agent's seccomp BPF filter (E4.3,
// exec/proc face). It wraps elastic/go-seccomp-bpf, which handles the arch and
// cBPF assembly; plexus stays CGO-free (no libseccomp). See docs/implement-design
// §5.6 for the selection rationale.
package seccomp

// Profile is a seccomp policy: a default action plus a syscall list, lowered to a
// BPF filter and installed on the sandboxed process by Load (linux).
//
// The initial DefaultProfile is a DENYLIST of the well-known dangerous syscalls
// (sandbox-escape / kernel-attack surface) with default-allow — small, correct,
// and it does not break a normal Go agent. The mature hardening target is porting
// the full OCI/Docker default ALLOWLIST as data (a focused follow-up: that is data,
// not mechanism).
type Profile struct {
	// DefaultAllow true  => allow by default and DENY the listed syscalls (denylist).
	// DefaultAllow false => deny by default and ALLOW the listed syscalls (allowlist).
	DefaultAllow bool
	Syscalls     []string
}

// DefaultProfile denies the syscalls a dev-task agent never needs but that enable
// sandbox escape or kernel attacks. Default-allow keeps ordinary work and the Go
// runtime unbroken. Names are all long-established (present on amd64 and arm64).
func DefaultProfile() Profile {
	return Profile{
		DefaultAllow: true,
		Syscalls: []string{
			// namespaces / mount — container escape. clone3 is included because it can
			// create namespaces like unshare (blocking only unshare leaves a hole); the
			// new mount API (fsopen/…/open_tree) is a second mount path the old `mount`
			// block would otherwise miss.
			"mount", "umount2", "unshare", "clone3", "setns", "pivot_root", "chroot",
			"fsopen", "fsconfig", "fsmount", "move_mount", "open_tree",
			// io_uring — it can perform fs/net operations that never surface as filtered
			// syscalls (a well-known seccomp bypass), so deny setup outright.
			"io_uring_setup", "io_uring_enter", "io_uring_register",
			// tracing / other-process introspection
			"ptrace", "process_vm_readv", "process_vm_writev", "kcmp",
			// kernel keyring
			"keyctl", "add_key", "request_key",
			// bpf / perf / kernel modules / kexec
			"bpf", "perf_event_open",
			"kexec_load", "init_module", "finit_module", "delete_module",
			// power / time / accounting / quota
			"reboot", "swapon", "swapoff",
			"settimeofday", "clock_settime", "acct", "quotactl",
			"userfaultfd",
		},
	}
}
