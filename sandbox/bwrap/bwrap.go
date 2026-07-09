package bwrap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// EnvPolicy carries the JSON-encoded per-agent Policy from Phase-0 Setup down to the
// agent's self-reexec (Phase 1), which builds the bwrap args from it. Empty means
// DefaultPolicy (the dev baseline).
const EnvPolicy = "PLEXUS_SANDBOX_POLICY"

// ProviderFromEnv builds a bwrap provider from EnvPolicy: the per-agent Policy when
// Setup set it, else the permissive DefaultPolicy.
func ProviderFromEnv() (*Provider, error) {
	js := os.Getenv(EnvPolicy)
	if js == "" {
		return New(), nil
	}
	var p Policy
	if err := json.Unmarshal([]byte(js), &p); err != nil {
		return nil, fmt.Errorf("bwrap: bad %s: %w", EnvPolicy, err)
	}
	return NewWithPolicy(p), nil
}

// ExtractBwrap materializes the embedded bwrap binary on disk and returns an absolute
// path to it. It is content-addressed: the binary is cached at
// ~/.plexus/cache/bwrap/bwrap-<sha256[:16]> and reused across launches. A sandboxed
// process execs INTO this binary and so can never clean it up — a per-launch temp file
// would therefore leak ~1-2 MB on every start; the cache extracts once and reuses.
// Extraction is atomic (write a sibling temp, chmod, rename) so concurrent launches race
// safely. When the cache dir can't be used (e.g. a read-only HOME) it falls back to a
// per-launch temp file so sandboxing still works.
func ExtractBwrap() (string, error) {
	if len(bwrapBinary) == 0 {
		return "", fmt.Errorf("sandboxed mode is not supported on this OS/Arch combination (bwrap binary not embedded)")
	}

	sum := sha256.Sum256(bwrapBinary)
	name := "bwrap-" + hex.EncodeToString(sum[:8]) // 16 hex chars — disambiguates builds

	if dir, err := bwrapCacheDir(); err == nil {
		cached := filepath.Join(dir, name)
		// Reuse a correctly-sized, executable cache hit.
		if fi, err := os.Stat(cached); err == nil && fi.Size() == int64(len(bwrapBinary)) && fi.Mode()&0o111 != 0 {
			return cached, nil
		}
		if err := writeExecutable(cached, bwrapBinary); err == nil {
			return cached, nil
		}
		// fall through to a temp file on any cache-write failure
	}
	return writeTempExecutable(bwrapBinary)
}

// bwrapCacheDir returns ~/.plexus/cache/bwrap (created), or an error if HOME is unusable.
func bwrapCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".plexus", "cache", "bwrap")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// writeExecutable atomically writes data to path with mode 0755: write a sibling temp in
// the same dir, chmod, then rename (atomic on one filesystem). Concurrent writers race
// harmlessly — the last rename wins and every result carries identical content.
func writeExecutable(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bwrap-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// writeTempExecutable writes data to a fresh per-launch temp file (the no-cache fallback).
func writeTempExecutable(data []byte) (string, error) {
	tmpFile, err := os.CreateTemp("", "plexus-bwrap-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file for bwrap: %w", err)
	}
	defer tmpFile.Close()
	if _, err := tmpFile.Write(data); err != nil {
		return "", fmt.Errorf("failed to write embedded bwrap: %w", err)
	}
	if err := os.Chmod(tmpFile.Name(), 0o755); err != nil {
		return "", fmt.Errorf("failed to chmod bwrap binary: %w", err)
	}
	return filepath.Abs(tmpFile.Name())
}

type Provider struct {
	policy    Policy
	hasPolicy bool
}

// New returns a bwrap provider that applies DefaultPolicy (the permissive dev
// baseline).
func New() *Provider {
	return &Provider{}
}

// NewWithPolicy returns a bwrap provider that applies a specific per-agent Policy —
// the enforce path (Phase 1), where Setup has assembled the agent's minimal rootfs,
// provision binds, masks, and sealed env (E4.4/E4.6).
func NewWithPolicy(p Policy) *Provider {
	return &Provider{policy: p, hasPolicy: true}
}

func (p *Provider) Name() string {
	return "bwrap"
}

// Enter extracts bwrap, constructs the isolation arguments, and performs syscall.Exec.
func (p *Provider) Enter(ticketPath string, extraArgs []string) error {
	bwrapPath, err := ExtractBwrap()
	if err != nil {
		return fmt.Errorf("failed to extract embedded bwrap: %w", err)
	}

	// Isolation args come from the translation layer (E4.2): the per-agent Policy
	// when Setup provided one, else DefaultPolicy (dev baseline). The ticket bind is
	// the sandbox handshake (mechanism), not isolation policy, so it is appended here.
	policy := DefaultPolicy()
	if p.hasPolicy {
		policy = p.policy
	}
	bwrapArgs := []string{bwrapPath}
	bwrapArgs = append(bwrapArgs, Translate(policy)...)

	// --clearenv (from a sealed-env Policy) drops EVERYTHING, including the sandbox's
	// own control channel — the ticket, the per-agent Policy, and the egress fd /
	// network env Setup handed down. Re-inject the PLEXUS_ infra namespace after the
	// clear (later --setenv wins) so sealing the agent's env doesn't brick the
	// handshake or the egress proxy. Only PLEXUS_ vars survive; host secrets are still
	// dropped.
	if policy.Clearenv {
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "PLEXUS_") {
				if k, v, ok := strings.Cut(kv, "="); ok {
					bwrapArgs = append(bwrapArgs, "--setenv", k, v)
				}
			}
		}
	}

	bwrapArgs = append(bwrapArgs, "--bind", ticketPath, ticketPath)

	if len(extraArgs) > 0 {
		bwrapArgs = append(bwrapArgs, extraArgs...)
	}

	// The agent argv is this process's own os.Args, but argv[0] is resolved to the
	// absolute self-path: bwrap execvp's it INSIDE the sandbox (often after --chdir to
	// the workspace), so a relative launch argv[0] (e.g. `dl/plexus-linux-amd64`) would
	// otherwise fail to resolve. This covers both the fenced and the degraded entry paths.
	agentArgv := append([]string(nil), os.Args...)
	if exe, err := os.Executable(); err == nil {
		agentArgv[0] = exe
	}
	bwrapArgs = append(bwrapArgs, "--")
	bwrapArgs = append(bwrapArgs, agentArgv...)

	return syscall.Exec(bwrapPath, bwrapArgs, os.Environ())
}
