package effector

// Policy decides whether invoking an effector requires human approval
// (Yield-for-Approval, §5.4.3 / §5.7.4). It is intentionally small and
// overridable: the brain consults it before dispatch, and the delegation
// envelope uses it to exclude approval-required effectors entirely.
type Policy interface {
	// RequiresApproval reports whether e must be gated behind human approval
	// before it may run.
	RequiresApproval(e Effector) bool
}

// DefaultPolicy implements the §5.7.4 default routing keyed PURELY on the risk
// tag — no name-matching:
//
//   - Read          -> auto-allowed (no approval).
//   - Write         -> auto-allowed (mutations are reversible via VCS and
//     sandbox-confined).
//   - ExecSandboxed -> auto-allowed (contained build / test / lint), which is
//     what lets a delegation self-verify inside its sandbox.
//   - ExecArbitrary -> approval required (generic shell, network, deploy).
//
// A generic shell is tagged ExecArbitrary (gated); specific tools carry specific
// tags. There is no VCS name-matching: tier is decided by the tag alone.
type DefaultPolicy struct{}

// RequiresApproval applies the default §5.7.4 routing.
func (DefaultPolicy) RequiresApproval(e Effector) bool {
	switch e.Risk() {
	case Read, Write, ExecSandboxed:
		return false
	case ExecArbitrary:
		return true
	default:
		// Unknown/future tags are treated conservatively as the highest tier.
		return true
	}
}

// PolicyFunc adapts an ordinary function to the Policy interface, for callers
// that want to supply a one-off override without declaring a type.
type PolicyFunc func(e Effector) bool

// RequiresApproval calls the underlying function.
func (f PolicyFunc) RequiresApproval(e Effector) bool { return f(e) }
