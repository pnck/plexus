package brain

import _ "embed"

// principlesPrompt is the built-in kernel prompt (§5.7.11): a set of operating
// principles common to every plexus agent, independent of the role card and
// injected at L1 BEFORE it. It is embedded from principles.txt so the prose is
// editable on its own and versioned with the code; it is NOT user- or
// role-configurable — it encodes the cognitive-loop invariants (authority
// layering, effector/delegation use, memory, and the §5.7.10 "task truth must be
// emitted" rule) the loop's correctness depends on.
//
//go:embed principles.txt
var principlesPrompt string
