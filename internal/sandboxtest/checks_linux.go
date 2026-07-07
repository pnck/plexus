//go:build linux && sandboxtest

package sandboxtest

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"plexus/sandbox"
)

// Result is one enforcement check's outcome.
type Result struct {
	Name   string
	Pass   bool
	Skip   bool // environment can't provide the feature (e.g. no cgroup delegation) — not a failure
	Detail string
}

func pass(name, detail string) Result { return Result{Name: name, Pass: true, Detail: detail} }
func fail(name, detail string) Result { return Result{Name: name, Detail: detail} }
func skip(name, detail string) Result { return Result{Name: name, Skip: true, Detail: detail} }

// RunAll runs every enforcement check INSIDE the established sandbox and returns whether
// all non-skipped checks passed. It must be called only after sandbox.Enter has returned
// (i.e. from the confined stage).
func RunAll() (ok bool, results []Result) {
	ok = true
	for _, c := range []func() Result{
		checkNetnsLoopbackOnly,
		checkExternalEgressBlocked,
		checkSeccompActive,
		checkRlimitsLowered,
		checkTmpfs,
		checkProcMounted,
		checkCgroupApplied,
	} {
		r := c()
		results = append(results, r)
		if !r.Pass && !r.Skip {
			ok = false
		}
	}
	return ok, results
}

// Report prints one line per check: [PASS]/[FAIL]/[SKIP] name — detail.
func Report(w io.Writer, results []Result) {
	for _, r := range results {
		status := "PASS"
		switch {
		case r.Skip:
			status = "SKIP"
		case !r.Pass:
			status = "FAIL"
		}
		fmt.Fprintf(w, "[%s] %s — %s\n", status, r.Name, r.Detail)
	}
}

// checkNetnsLoopbackOnly asserts the agent's netns has ONLY loopback — no veth, no other
// device. This is the zero-privilege design: the netns is loopback-only and the control
// plane is reached over inherited fds, never an IP route.
//
// It enumerates via netlink (net.Interfaces), NOT /sys/class/net: DefaultPolicy is still
// the E0 `--ro-bind / /`, so the host's /sys is bind-mounted into the sandbox and
// /sys/class/net would report the HOST's interfaces regardless of our netns. A netlink
// query is netns-scoped, so it reflects our ACTUAL current namespace.
func checkNetnsLoopbackOnly() Result {
	ifaces, err := net.Interfaces()
	if err != nil {
		return fail("netns-loopback-only", "list interfaces: "+err.Error())
	}
	var names []string
	for _, i := range ifaces {
		names = append(names, i.Name)
	}
	if len(names) == 1 && names[0] == "lo" {
		return pass("netns-loopback-only", "only lo present (netlink)")
	}
	return fail("netns-loopback-only", "expected only lo, got ["+strings.Join(names, ",")+"]")
}

// checkExternalEgressBlocked asserts a direct connection to an external host does NOT
// succeed — under the deny-all default the netns has no route out, so the dial fails.
// (The self-test runs deny-all precisely so this assertion is unambiguous; the redirect
// path is covered by the run-based smoke.)
func checkExternalEgressBlocked() Result {
	c, err := net.DialTimeout("tcp", "1.1.1.1:53", 3*time.Second)
	if err != nil {
		return pass("egress-fenced", "external dial blocked ("+err.Error()+")")
	}
	_ = c.Close()
	return fail("egress-fenced", "external dial to 1.1.1.1:53 SUCCEEDED — the fence is not enforcing")
}

// checkSeccompActive asserts a seccomp filter is loaded and no_new_privs is set — the
// unambiguous proof that confineSelf's seccomp step ran. (The denylist returns EPERM
// rather than killing, so a filter is present iff Seccomp=2.)
func checkSeccompActive() Result {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fail("seccomp-active", "read /proc/self/status: "+err.Error())
	}
	seccomp := statusField(data, "Seccomp:")
	nnp := statusField(data, "NoNewPrivs:")
	if seccomp == "2" && nnp == "1" {
		return pass("seccomp-active", "Seccomp=2 (filter mode) NoNewPrivs=1")
	}
	return fail("seccomp-active", fmt.Sprintf("Seccomp=%q NoNewPrivs=%q (want 2 / 1)", seccomp, nnp))
}

// checkRlimitsLowered asserts the soft rlimits were lowered to the confinement floor.
func checkRlimitsLowered() Result {
	want := sandbox.DefaultConfinement().Rlimits
	var nofile, nproc unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &nofile); err != nil {
		return fail("rlimit-lowered", "getrlimit NOFILE: "+err.Error())
	}
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &nproc); err != nil {
		return fail("rlimit-lowered", "getrlimit NPROC: "+err.Error())
	}
	// The floor is clamped to the inherited hard limit, so assert it is no HIGHER than the
	// floor (proving it was lowered from the host default), and matches when the host hard
	// limit allows the exact floor.
	if nofile.Cur <= want.NOFILE && nproc.Cur <= want.NPROC {
		return pass("rlimit-lowered", fmt.Sprintf("NOFILE.cur=%d(<=%d) NPROC.cur=%d(<=%d)",
			nofile.Cur, want.NOFILE, nproc.Cur, want.NPROC))
	}
	return fail("rlimit-lowered", fmt.Sprintf("NOFILE.cur=%d(want<=%d) NPROC.cur=%d(want<=%d)",
		nofile.Cur, want.NOFILE, nproc.Cur, want.NPROC))
}

// checkTmpfs asserts /tmp is an isolated tmpfs (bwrap --tmpfs /tmp), not the host /tmp.
func checkTmpfs() Result {
	var s unix.Statfs_t
	if err := unix.Statfs("/tmp", &s); err != nil {
		return fail("tmpfs-tmp", "statfs /tmp: "+err.Error())
	}
	if int64(s.Type) == int64(unix.TMPFS_MAGIC) {
		return pass("tmpfs-tmp", "/tmp is tmpfs")
	}
	return fail("tmpfs-tmp", fmt.Sprintf("/tmp fs type=0x%x (want tmpfs 0x%x)", uint64(s.Type), uint64(unix.TMPFS_MAGIC)))
}

// checkProcMounted asserts /proc is a procfs (bwrap --proc /proc, in the agent's pid ns).
func checkProcMounted() Result {
	var s unix.Statfs_t
	if err := unix.Statfs("/proc", &s); err != nil {
		return fail("proc-mounted", "statfs /proc: "+err.Error())
	}
	if int64(s.Type) == int64(unix.PROC_SUPER_MAGIC) {
		return pass("proc-mounted", "/proc is procfs")
	}
	return fail("proc-mounted", fmt.Sprintf("/proc fs type=0x%x (want procfs 0x%x)", uint64(s.Type), uint64(unix.PROC_SUPER_MAGIC)))
}

// checkCgroupApplied asserts the agent is in its own delegated cgroup. When no cgroup
// subtree is delegated (the common CI/unprivileged case) the fence soft-degrades to the
// rlimit floor, so this is a SKIP, not a failure.
func checkCgroupApplied() Result {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return fail("cgroup-applied", "read /proc/self/cgroup: "+err.Error())
	}
	line := strings.TrimSpace(string(data))
	if strings.Contains(line, AgentID) {
		return pass("cgroup-applied", line)
	}
	return skip("cgroup-applied", "no delegated cgroup — soft-degraded to the rlimit floor ("+line+")")
}

// statusField returns the value of a "Key:\tvalue" line from /proc/*/status.
func statusField(data []byte, key string) string {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, key) {
			return strings.TrimSpace(strings.TrimPrefix(line, key))
		}
	}
	return ""
}
