package effector

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Registry holds all effectors available to an agent's brain (built-in +
// MCP-sourced). It is owned by the brain. A delegation never receives the
// Registry directly — it receives a filtered Capabilities handle via
// DelegationEnvelope.
//
// Registry is safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]Effector
	policy Policy
}

// NewRegistry creates an empty Registry governed by the given Policy. A nil
// policy defaults to DefaultPolicy.
func NewRegistry(policy Policy) *Registry {
	if policy == nil {
		policy = DefaultPolicy{}
	}
	return &Registry{
		byName: make(map[string]Effector),
		policy: policy,
	}
}

// Register adds (or replaces) an effector by name.
func (r *Registry) Register(e Effector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[e.Name()] = e
}

// Get returns the effector registered under name.
func (r *Registry) Get(name string) (Effector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byName[name]
	return e, ok
}

// List returns all registered effectors, sorted by name for stable output.
func (r *Registry) List() []Effector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Effector, 0, len(r.byName))
	for _, e := range r.byName {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// RequiresApproval reports whether invoking the named effector requires human
// approval per the registry's policy. Unknown effectors are reported as
// requiring approval (conservative default).
func (r *Registry) RequiresApproval(name string) bool {
	e, ok := r.Get(name)
	if !ok {
		return true
	}
	return r.policy.RequiresApproval(e)
}

// DelegationEnvelope returns the mediated capability handle (能力封套) for a
// delegation. It exposes ONLY the approval-free, shareable subset of the
// registry: EXCLUDED are every approval-required effector (per the registry
// policy — i.e. whose effects exceed the permitted set) and every agent-private
// effector (memory; see AgentPrivate), while approval-free contained exec
// (exec.boxed: build / test / lint) is INCLUDED so the delegation can self-verify
// inside its sandbox (§5.7.4). Filtering on RequiresApproval (a subset test on
// effects) excludes exec.arbitrary and includes exec.boxed without any
// name-matching; the AgentPrivate check keeps a delegation from gaining
// persistent memory (§5.7.7). The handle is a snapshot taken at call time.
func (r *Registry) DelegationEnvelope() Capabilities {
	r.mu.RLock()
	defer r.mu.RUnlock()
	permitted := make(map[string]Effector)
	for name, e := range r.byName {
		if r.policy.RequiresApproval(e) || isAgentPrivate(e) {
			continue
		}
		permitted[name] = e
	}
	return &envelope{permitted: permitted}
}

// Capabilities is the mediated, filtered handle handed to a delegation (the 能力
// 封套). It exposes ONLY the delegation-permitted subset and DENIES
// out-of-envelope calls. An out-of-envelope Invoke returns an error — the
// delegation reports "need X but not permitted" in its Result; it does NOT
// escalate (it has no inbound, so it cannot obtain approval).
type Capabilities interface {
	// List returns the effectors inside the envelope.
	List() []Effector
	// Invoke runs a permitted effector. An effector outside the envelope returns
	// an *OutOfEnvelopeError (NOT an escalation).
	Invoke(ctx context.Context, name string, args json.RawMessage) (Result, error)
}

// OutOfEnvelopeError is returned by Capabilities.Invoke when a delegation
// requests an effector that is not inside its capability envelope.
type OutOfEnvelopeError struct {
	Name string
}

func (e *OutOfEnvelopeError) Error() string {
	return fmt.Sprintf("effector %q is outside the delegation capability envelope (not permitted, no escalation)", e.Name)
}

// envelope is the concrete Capabilities implementation backing DelegationEnvelope.
type envelope struct {
	permitted map[string]Effector
}

// List returns the permitted effectors, sorted by name.
func (e *envelope) List() []Effector {
	out := make([]Effector, 0, len(e.permitted))
	for _, eff := range e.permitted {
		out = append(out, eff)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Invoke runs a permitted effector or returns an *OutOfEnvelopeError.
func (e *envelope) Invoke(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	eff, ok := e.permitted[name]
	if !ok {
		return Result{}, &OutOfEnvelopeError{Name: name}
	}
	return eff.Invoke(ctx, args)
}
