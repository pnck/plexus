// Package sandboxtest holds the in-sandbox enforcement self-test: code that runs INSIDE
// a fully established sandbox and asserts each isolation property actually HOLDS — not
// just that the sandbox started. `run --sandbox` reaching the confined stage is a
// liveness signal; these checks are the enforcement signal (the netns is loopback-only,
// external egress is fenced, seccomp is active, rlimits are lowered, /tmp is a tmpfs,
// /proc is the pid-ns proc, and the cgroup is applied when delegation is available).
//
// The checks (and the integration test that drives them) are gated behind the
// `sandboxtest` build tag so they NEVER enter production or local default builds; CI
// runs them in a dedicated job on a real unprivileged-userns runner. This file carries
// no build tag so the package is always non-empty for `go build ./...`.
package sandboxtest

// AgentID is the sandbox AgentID the self-test uses. The per-agent cgroup (when cgroup
// delegation is available) is named after it, so the cgroup check looks for it in
// /proc/self/cgroup. Shared by the self-test command and the checks so they never drift.
const AgentID = "selftest"

// EnvSelfTestMask names an absolute host path the CI job plants a file under and hands
// to `sandbox-selftest --mask <path>`; the fs-masked check reads it and asserts the path
// is an empty tmpfs inside the sandbox (host contents hidden). Unset => the check SKIPs.
const EnvSelfTestMask = "PLEXUS_SELFTEST_MASK"
