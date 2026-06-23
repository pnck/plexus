package llm

// Reasoning effort is a single neutral knob the agent exposes for how hard a
// model should think. It is deliberately a SUPERSET of any one provider's API:
// the semantic levels are richer (and more numerous) than what any backend
// offers, and each provider maps/clamps them to its own range — never the other
// way round. So adding a backend with finer control (or a new high tier) needs
// no new vocabulary here, and a coarse backend just collapses the extra tiers.
//
// Ordered low→high intensity. Empty string ("") means "do not set it" (off).
const (
	EffortMinimal = "minimal"
	EffortLow     = "low"
	EffortMedium  = "medium"
	EffortHigh    = "high"
	EffortXHigh   = "xhigh"
	EffortMax     = "max"
)

// ReasoningEfforts is the ordered superset; index is the intensity rank.
var ReasoningEfforts = []string{EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

// EffortRank returns the 0-based intensity rank of a level, or -1 if unknown.
func EffortRank(level string) int {
	for i, e := range ReasoningEfforts {
		if e == level {
			return i
		}
	}
	return -1
}

// ValidEffort reports whether level is one of the known effort tiers.
func ValidEffort(level string) bool { return EffortRank(level) >= 0 }
