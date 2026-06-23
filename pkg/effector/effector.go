// Package effector defines the tool ("effector") abstraction and the policy
// layer around it (§5.7.4). An effector is a single tool the brain — or a
// delegation — can invoke: a direct action with no context window of its own.
//
// This package provides four things:
//
//   - The Effector interface and its RiskTag
//     (Read/Write/ExecSandboxed/ExecArbitrary) classification.
//   - A Registry of all effectors available to an agent's brain (built-in +
//     MCP-sourced).
//   - A Policy that decides which effectors require human approval, derived from
//     their risk tag.
//   - The delegation capability envelope (能力封套): a mediated, filtered handle
//     handed to a delegation that exposes ONLY the approval-free subset and denies
//     out-of-envelope calls. Delegations never hold an MCP client or the Registry
//     directly; they reach tools only through this handle.
package effector

import (
	"context"
	"encoding/json"
)

// RiskTag classifies an effector by its side-effect tier (§5.7.4). The tag
// feeds policy (approval routing) and per-role schema filtering; it is NOT a
// capability fence — a released arbitrary-exec shell can still do anything.
// ExecArbitrary is a superset and is always treated as the highest tier.
//
// Exec is split into two tiers so the delegation envelope can auto-include
// contained build/test (ExecSandboxed) while excluding arbitrary shell
// (ExecArbitrary) WITHOUT any name-matching: the tag alone carries the
// distinction.
type RiskTag int

const (
	// Read has no side effects (list / read / grep). Auto-allowed.
	Read RiskTag = iota
	// Write mutates the local workspace (reversible via VCS, sandbox-confined).
	Write
	// ExecSandboxed runs code CONFINED to the sandbox (build / test / lint):
	// bounded, no network. Approval-free, so it enters the delegation envelope.
	ExecSandboxed
	// ExecArbitrary runs unbounded/arbitrary code (generic shell, network,
	// deploy): the highest tier. Default policy routes it through
	// Yield-for-Approval, and the delegation envelope excludes it.
	ExecArbitrary
)

// String renders a RiskTag for logs and errors.
func (r RiskTag) String() string {
	switch r {
	case Read:
		return "Read"
	case Write:
		return "Write"
	case ExecSandboxed:
		return "ExecSandboxed"
	case ExecArbitrary:
		return "ExecArbitrary"
	default:
		return "Unknown"
	}
}

// Result is the outcome of an effector invocation. IsError signals a tool-level
// error (the tool ran but failed) which is fed back to the LLM for
// self-correction, as opposed to an infrastructure error returned as a Go error
// from Invoke.
type Result struct {
	// Content is the textual result fed back into the model's context.
	Content string
	// IsError marks a tool-level error for LLM self-correction.
	IsError bool
}

// AgentPrivate is an optional interface an effector implements to opt OUT of the
// delegation capability envelope even when it is approval-free. It marks tools
// that belong to the agent's brain alone — memory (mem_*/ltm_*) is agent-private
// because a delegation has no persistent memory (§5.7.7): its job is to run a
// lean, stateless LLM↔tools loop and return a distilled Result. An effector that
// does not implement this interface (or returns false) is treated as shareable.
type AgentPrivate interface {
	// AgentPrivate reports whether this effector is excluded from delegations.
	AgentPrivate() bool
}

// isAgentPrivate reports whether e has opted out of the delegation envelope.
func isAgentPrivate(e Effector) bool {
	ap, ok := e.(AgentPrivate)
	return ok && ap.AgentPrivate()
}

// Effector is one tool the brain (or a delegation) can invoke. Implementations
// must be safe for concurrent use.
type Effector interface {
	// Name is the unique tool identifier surfaced to the LLM.
	Name() string
	// Description is a human-readable hint for the model.
	Description() string
	// Risk reports the side-effect tier used by Policy.
	Risk() RiskTag
	// Schema is the JSON Schema object describing Invoke's arguments.
	Schema() json.RawMessage
	// Invoke runs the tool. A non-nil error is an infrastructure/transport
	// failure; a tool-level failure is reported via Result.IsError with a nil
	// error so it can be fed back to the model.
	Invoke(ctx context.Context, args json.RawMessage) (Result, error)
}
