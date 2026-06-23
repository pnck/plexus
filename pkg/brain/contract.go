// Package brain implements the agent's cognitive loop (E2.2) and the delegation
// sub-cognition primitive plus its delegation contract (E2.4), per §5.7 of the
// implementation design.
//
// The brain is an agent's SINGLETON cognition: it loads a role card, receives
// structured inbound messages, stamps each into an authority layer by its source
// channel (§5.7.3), composes a layered context window, drives the LLM gateway,
// and dispatches the model's intent — a final reply, an effector call (gated by
// an approval hook), or a delegate call that spawns a fresh delegation.
//
// A delegation is a lean, fresh-only LLM<->tools loop with no bus endpoint, no
// registry, no role card, no inbound and no persistent memory. Its only output
// is a distilled Result on a channel. These invariants are enforced structurally:
// spawnDelegation takes ONLY a gateway, a capability envelope, a Briefing, and a
// max-turns bound.
package brain

import (
	"encoding/json"

	"plexus/pkg/jsonschema"
)

// Briefing is the downward half of the delegation contract (§5.7.7): the only
// channel into a fresh delegation. It carries the objective, scope (including
// out-of-scope), context POINTERS (not payload — the delegation reads them
// itself), constraints, acceptance criteria, and the expected shape of the
// returned Result. For the delegation, the Briefing is its L1/L2 (objective ==
// instruction); content it reads is L3 data and must not be treated as higher
// instruction.
type Briefing struct {
	Objective   string   // what to accomplish
	Scope       string   // in-scope
	OutOfScope  string   // explicit out-of-scope — blast-radius containment
	Pointers    []string // file/doc paths the delegation reads itself (pointers, not payload)
	Constraints string   // invariants the delegation must hold
	Acceptance  string   // acceptance criteria
	ReturnSpec  string   // shape of the expected Result
}

// Result is the upward half of the delegation contract (§5.7.7): a schema-bound
// DISTILLATION, never a transcript (a child transcript fed back to the parent is
// reverse flood contamination). The brain absorbs this as the tool result of the
// delegate call.
type Result struct {
	Summary       string   `json:"summary"`        // distilled outcome
	Changes       []string `json:"changes"`        // change list
	Verified      string   `json:"verified"`       // pass/fail of any in-bubble self-check
	Decisions     string   `json:"decisions"`      // decisions and deviations
	OpenQuestions string   `json:"open_questions"` // open questions / "needed X but not permitted"
}

// DelegateToolName is the built-in tool the brain surfaces to its LLM (alongside
// the registry's effectors). delegate is the wire form of THIS delegation
// contract — not an effector (it spawns cognition, has no risk tag, and is
// intercepted, never Invoke'd) — so its schema lives here, with Briefing, rather
// than in the loop or the effector layer. The brain intercepts a call to it and
// spawns a fresh delegation. Exported so the chat host can recognize delegation
// in tool-activity callbacks and render it distinctly from an effector call.
const DelegateToolName = "delegate"

// delegateSchema is the JSON schema for the delegate tool's arguments. It is
// reflected from delegateArgs (jsonschema.For) — the struct, with its tags, is
// the single source of truth, exactly as for the built-in effectors. delegate is
// not an effector (it spawns cognition), but it describes its arguments to the
// LLM the same way.
func delegateSchema() json.RawMessage {
	return jsonschema.For[delegateArgs]()
}

// delegateArgs is the wire form of the delegate tool's arguments. It mirrors
// Briefing field-for-field (only the JSON wire names differ), so briefing() is a
// direct struct conversion; the desc/omitempty tags drive the reflected schema
// and do not affect that conversion (Go ignores tags when converting structs).
type delegateArgs struct {
	Objective   string   `json:"objective" desc:"What the delegation must accomplish."`
	Scope       string   `json:"scope,omitempty" desc:"In-scope work."`
	OutOfScope  string   `json:"out_of_scope,omitempty" desc:"Explicitly out-of-scope; do not touch."`
	Pointers    []string `json:"pointers,omitempty" desc:"File/doc paths the delegation reads itself (pointers, not payload)."`
	Constraints string   `json:"constraints,omitempty" desc:"Invariants the delegation must hold."`
	Acceptance  string   `json:"acceptance,omitempty" desc:"Acceptance criteria."`
	ReturnSpec  string   `json:"return_spec,omitempty" desc:"Shape of the expected distilled result."`
}

// briefing converts the wire args into a Briefing (the field-for-field mirror
// makes a direct conversion the canonical mapping).
func (a delegateArgs) briefing() Briefing {
	return Briefing(a)
}
