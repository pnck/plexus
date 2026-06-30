package effector

// Policy decides whether invoking an effector requires human approval
// (Yield-for-Approval, §5.4.3 / §5.7.4). It is intentionally small and
// overridable: the brain consults it before dispatch, and the delegation
// envelope uses it to exclude approval-required effectors entirely.
//
// E3.2: gating is a single predicate — a call is auto-allowed iff its effects
// are a SUBSET of the role's permitted set; anything beyond permitted routes to
// approval (and, in the cluster, escalates up the supervisory chain, E3.4). There
// is no "bounding"/three-state ceiling: "never, even with approval" is a hard
// guarantee that only bwrap can give (E3.5/E4), not the soft layer.
type Policy interface {
	// RequiresApproval reports whether e must be gated behind human approval
	// before it may run.
	RequiresApproval(e Effector) bool
}

// DefaultPermitted is the behavior-preserving default grant — the effects that
// were auto-allowed before the Effect rework: workspace read/write, contained
// exec, and secret read. run_command's ExecArbitrary is deliberately NOT in
// it, so it is gated exactly as before. SecretRead stays in (net-zero) but is
// now separable: a hardened role can drop it.
var DefaultPermitted = NewEffectSet(FSRead, FSWrite, ExecBoxed, SecretRead)

// DefaultPolicy gates any effector whose effects exceed DefaultPermitted. It is
// the registry's default when no role-specific permitted set is supplied.
//
//   - effects ⊆ DefaultPermitted -> auto-allowed.
//   - otherwise (e.g. run_command's exec.arbitrary) -> approval required.
//
// The empty effect set (internal cognition: mem_*/step_*) is ⊆ everything, so it
// is always auto-allowed.
type DefaultPolicy struct{}

// RequiresApproval applies the default §5.7.4 routing as a subset test.
func (DefaultPolicy) RequiresApproval(e Effector) bool {
	return !e.Effects().SubsetOf(DefaultPermitted)
}

// PermittedPolicy gates any effector whose effects exceed Permitted — a role's
// grant (E3.2/E3.3, sourced from the role card). Beyond permitted routes to
// approval/escalation. A zero Permitted set gates everything with any effect
// (callers wanting today's behavior use DefaultPolicy or DefaultPermitted).
type PermittedPolicy struct {
	Permitted EffectSet
}

// RequiresApproval reports whether e's effects exceed the permitted set.
func (p PermittedPolicy) RequiresApproval(e Effector) bool {
	return !e.Effects().SubsetOf(p.Permitted)
}

// PolicyFunc adapts an ordinary function to the Policy interface, for callers
// that want to supply a one-off override without declaring a type.
type PolicyFunc func(e Effector) bool

// RequiresApproval calls the underlying function.
func (f PolicyFunc) RequiresApproval(e Effector) bool { return f(e) }
