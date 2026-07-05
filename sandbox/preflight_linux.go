//go:build linux

package sandbox

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"plexus/sandbox/caps"
)

// Preflight is the single up-front probe for `--sandbox`: it checks EVERY feature the
// full sandbox needs and, if any HARD requirement is missing, returns one actionable
// error listing them all at once — instead of letting a downstream stage (bwrap, a
// netlink call) fail with a raw, context-free message.
//
// Two hard requirements:
//   - unprivileged user namespaces — bwrap (Phase 1) cannot build its mount/user/pid
//     namespaces without them; when the host disables them, `unshare(CLONE_NEWUSER)`
//     is refused and the whole sandbox is impossible;
//   - the host capabilities the net fence + IP_TRANSPARENT egress sockets need
//     (CAP_NET_ADMIN + CAP_SYS_ADMIN), which plexus raises at startup from its
//     permitted set.
//
// cgroup delegation is deliberately NOT a hard gate: when it is unavailable (e.g. a
// non-privileged container) the executor degrades to the rlimit floor (flow doc §7),
// so the sandbox still stands. On success Preflight RAISES the required caps
// (permitted -> effective) so the Phase-0 setup calls succeed.
func Preflight(required caps.Set) error {
	var problems []string

	if err := probeUserns(); err != nil {
		problems = append(problems, "unprivileged user namespaces are unavailable ("+err.Error()+
			") — enable them (`sysctl -w kernel.unprivileged_userns_clone=1` and/or "+
			"`sysctl -w user.max_user_namespaces=15000`) or run on a host/container that permits them")
	}

	if m := caps.Missing(required); !m.Empty() {
		problems = append(problems, "missing capabilities "+m.Describe()+
			" — grant them (`setcap cap_net_admin,cap_sys_admin+ep <plexus binary>`) or run as root")
	}

	if len(problems) > 0 {
		return fmt.Errorf("sandbox preflight failed — `--sandbox` establishes the full sandbox and needs:\n  - %s",
			strings.Join(problems, "\n  - "))
	}

	// Everything is present; raise the caps the privileged Phase-0 setup will use.
	return caps.Ensure(required)
}

// probeUserns tests the exact kernel gate bwrap depends on: it forks a child asking
// the kernel for a new user namespace. If unprivileged user namespaces are disabled,
// the clone is refused at Start() (EPERM/EINVAL) — without touching THIS process's
// namespaces. The child itself is irrelevant (it re-execs plexus with an unknown arg
// and exits); we only care whether the clone was permitted.
func probeUserns() error {
	cmd := exec.Command("/proc/self/exe", "__plexus_userns_probe")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
	// Leave Stdout/Stderr nil so the throwaway child's output is discarded to /dev/null.
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Wait()
	return nil
}
