//go:build linux

package seccomp

import (
	"fmt"

	elastic "github.com/elastic/go-seccomp-bpf"
)

func (p Profile) toPolicy() elastic.Policy {
	def, listAction := elastic.ActionErrno, elastic.ActionAllow
	if p.DefaultAllow {
		def, listAction = elastic.ActionAllow, elastic.ActionErrno
	}
	return elastic.Policy{
		DefaultAction: def,
		Syscalls:      []elastic.SyscallGroup{{Action: listAction, Names: p.Syscalls}},
	}
}

// Load assembles the profile into a seccomp BPF filter and installs it on the
// CURRENT process — all threads via TSYNC (required for the multi-threaded Go
// runtime) — after setting no_new_privs. Call it in the sandbox phase, before the
// agent does untrusted work.
func Load(p Profile) error {
	if !elastic.Supported() {
		return fmt.Errorf("seccomp: unsupported kernel")
	}
	f := elastic.Filter{
		NoNewPrivs: true,
		Flag:       elastic.FilterFlagTSync,
		Policy:     p.toPolicy(),
	}
	if err := elastic.LoadFilter(f); err != nil {
		return fmt.Errorf("seccomp: load filter: %w", err)
	}
	return nil
}

// Validate assembles the profile to BPF WITHOUT installing it — for tests and
// launch-time preflight (catches an unknown syscall name for the build arch).
func Validate(p Profile) error {
	pol := p.toPolicy()
	if err := pol.Validate(); err != nil {
		return err
	}
	_, err := pol.Assemble()
	return err
}
