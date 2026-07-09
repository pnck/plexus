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
		checkNetnsFenced,
		checkNoCaps,
		checkExternalEgressBlocked,
		checkSeccompActive,
		checkRlimitsLowered,
		checkTmpfs,
		checkProcMounted,
		checkFsMasked,
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

// checkNetnsFenced asserts the agent is in its own per-agent netns with exactly the fence
// devices: loopback + the single agent veth to the CP. The host netns would show many
// devices (eth0, docker0, ...); a fence that failed to build would show none. It
// enumerates via netlink (net.Interfaces), NOT /sys/class/net — the host's /sys is
// bind-mounted in (DefaultPolicy is still `--ro-bind / /`), so /sys/class/net would
// report the HOST interfaces; a netlink query is netns-scoped.
func checkNetnsFenced() Result {
	ifaces, err := net.Interfaces()
	if err != nil {
		return fail("netns-fenced", "list interfaces: "+err.Error())
	}
	var nonLo []string
	for _, i := range ifaces {
		if i.Name != "lo" {
			nonLo = append(nonLo, i.Name)
		}
	}
	switch len(nonLo) {
	case 1:
		return pass("netns-fenced", "isolated netns (lo + veth "+nonLo[0]+")")
	case 0:
		return fail("netns-fenced", "no veth in the netns — the network fence was not built")
	default:
		return fail("netns-fenced", "not a fenced netns — sees host interfaces ["+strings.Join(nonLo, ",")+"]")
	}
}

// checkNoCaps asserts the agent holds NO capabilities (effective set empty) — the
// tamper-proof guarantee: the launcher's CAP_NET_ADMIN is dropped (bwrap --cap-drop ALL)
// before the agent runs, so no subprocess can reconfigure the fence or escape the netns.
func checkNoCaps() Result {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fail("no-caps", "read /proc/self/status: "+err.Error())
	}
	eff := statusField(data, "CapEff:")
	if strings.Trim(eff, "0") == "" && eff != "" {
		return pass("no-caps", "CapEff="+eff+" (agent holds no capabilities)")
	}
	return fail("no-caps", "CapEff="+eff+" (want all-zero — the agent must be cap-dropped)")
}

// checkExternalEgressBlocked asserts a direct connection to an external host does NOT
// succeed — under the deny-all default the nft `policy drop` drops the packet (the veth
// gives the netns a default route, so it is the fence, not the absence of a route, that
// blocks). (The self-test runs deny-all precisely so this assertion is unambiguous; the
// redirect path is covered by the run-based smoke.)
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

// checkFsMasked asserts a host path handed via --mask is HIDDEN inside the sandbox: an
// empty tmpfs overlays it, so the host contents planted there (a "secret" file) are
// gone. The path comes from EnvSelfTestMask, which the CI job sets alongside
// `sandbox-selftest --mask <path>`; without it, masking isn't exercised this run => SKIP.
// This is the runtime proof that the bwrap Mask mechanism (Policy.Mask -> --tmpfs)
// actually hides sensitive host paths (the fs-isolation guarantee).
func checkFsMasked() Result {
	masked := os.Getenv(EnvSelfTestMask)
	if masked == "" {
		return skip("fs-masked", "no "+EnvSelfTestMask+" set — masking not exercised this run")
	}
	entries, err := os.ReadDir(masked)
	if err != nil {
		return fail("fs-masked", "read "+masked+": "+err.Error())
	}
	if len(entries) == 0 {
		return pass("fs-masked", masked+" is an empty tmpfs (planted host secret hidden)")
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return fail("fs-masked", masked+" is NOT masked — host contents visible ["+strings.Join(names, ",")+"]")
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
