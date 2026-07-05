package sandbox

import (
	"fmt"
	"log/slog"
	"os"
)

// Provider defines the interface for different sandboxing technologies (e.g., bwrap, gvisor).
type Provider interface {
	Name() string
	Enter(ticketPath string, extraArgs []string) error
}

// EnvTicket names the one-time handover ticket path. Its presence in the environment
// is how a process knows it is already INSIDE the sandbox (Phase 2) rather than on
// the host — the sandbox-entry state machine keys on it.
const EnvTicket = "PLEXUS_SANDBOX_TICKET"

// EnterIfRequested evaluates the sandbox flag and the current execution state.
// - If sandboxed == false, it returns immediately.
// - If on Host: it generates the ticket, calls the provider, and syscall.Execs.
// - If in Sandbox: it verifies and consumes the ticket, returning nil to allow execution to proceed.
func EnterIfRequested(sandboxed bool, provider Provider, extraArgs []string) error {
	if !sandboxed {
		return nil
	}

	if provider == nil {
		return fmt.Errorf("sandbox mode requested but no sandbox provider was configured")
	}

	ticketPath := os.Getenv(EnvTicket)
	if ticketPath == "" {
		// --- HOST PHASE ---

		// 1. Generate one-time handover ticket
		path, err := GenerateTicket()
		if err != nil {
			return fmt.Errorf("failed to generate sandbox ticket: %w", err)
		}

		os.Setenv(EnvTicket, path)

		slog.Info("Hollowing out process and entering sandbox...", "provider", provider.Name(), "ticket", path)

		// Delegate to specific sandbox implementation
		if err := provider.Enter(path, extraArgs); err != nil {
			return fmt.Errorf("sandbox transition failed via %s: %w", provider.Name(), err)
		}

		return nil
	}

	// --- SANDBOX PHASE ---

	if err := VerifyAndConsumeTicket(ticketPath); err != nil {
		return fmt.Errorf("FATAL: %w", err)
	}

	// Phase 2 self-confinement (flow doc §4): now that bwrap has built the
	// namespaces, the agent shrinks its own surface — lower rlimits, then load the
	// seccomp filter — before it runs any untrusted work. Both are unprivileged and
	// irreversible; this is the last thing before the cognitive loop.
	if err := confineSelf(DefaultConfinement()); err != nil {
		return fmt.Errorf("FATAL: phase-2 confinement: %w", err)
	}

	slog.Info("[Sandbox] Verified deterministic entry into isolated environment!")
	return nil
}
