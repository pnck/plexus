//go:build linux && sandboxtest

package sandboxtest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestSandboxEnforcement drives the real sandbox on a real unprivileged-userns host: it
// runs the sandboxtest-built plexus binary as `sandbox-selftest`, which enters the full
// launch->fence->jail->confine chain and asserts each isolation property from inside.
// The binary path comes from PLEXUS_BIN (the CI enforcement job sets it to the
// -tags sandboxtest build). Requires unprivileged user namespaces; skipped without a
// binary so `go test -tags sandboxtest ./...` does not fail on a userns-less host.
func TestSandboxEnforcement(t *testing.T) {
	bin := os.Getenv("PLEXUS_BIN")
	if bin == "" {
		t.Skip("PLEXUS_BIN not set — build plexus with `-tags sandboxtest` and point PLEXUS_BIN at it")
	}

	args := []string{"sandbox-selftest"}
	if m := os.Getenv(EnvSelfTestMask); m != "" {
		args = append(args, "--mask", m) // exercise fs masking: this host path must be hidden inside
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ() // propagate EnvSelfTestMask into the sandbox so the fs-masked check can read it
	out, err := cmd.CombinedOutput()
	t.Logf("sandbox-selftest output:\n%s", out)
	if err != nil {
		t.Fatalf("sandbox-selftest exited non-zero (%v) — an isolation check failed or the chain broke; see output above", err)
	}

	s := string(out)
	if strings.Contains(s, "[FAIL]") {
		t.Fatalf("one or more enforcement checks reported [FAIL] — see output above")
	}
	// The core enforcement properties must be present and not skipped (a SKIP here would
	// mean the check silently didn't run).
	for _, name := range []string{"netns-loopback-only", "egress-fenced", "seccomp-active", "rlimit-lowered", "tmpfs-tmp", "proc-mounted"} {
		if !strings.Contains(s, "[PASS] "+name) {
			t.Errorf("expected [PASS] %s in the self-test output", name)
		}
	}
	// When a mask probe is provided, masking MUST hide it (fs-isolation guarantee).
	if os.Getenv(EnvSelfTestMask) != "" && !strings.Contains(s, "[PASS] fs-masked") {
		t.Errorf("expected [PASS] fs-masked when %s is set — masking must hide the host path", EnvSelfTestMask)
	}
}
