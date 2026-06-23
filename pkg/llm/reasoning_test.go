package llm

import "testing"

// The agent's effort vocabulary is a strict, ordered superset: it includes
// OpenAI's range (minimal..high) AND higher tiers (xhigh, max) so a backend with
// finer control is honored and a coarse one clamps — never the reverse.
func TestReasoningEffortsAreAnOrderedSuperset(t *testing.T) {
	want := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	if len(ReasoningEfforts) != len(want) {
		t.Fatalf("ReasoningEfforts = %v, want %v", ReasoningEfforts, want)
	}
	for i, e := range want {
		if ReasoningEfforts[i] != e {
			t.Fatalf("ReasoningEfforts[%d] = %q, want %q", i, ReasoningEfforts[i], e)
		}
		if EffortRank(e) != i {
			t.Fatalf("EffortRank(%q) = %d, want %d", e, EffortRank(e), i)
		}
	}
	// Strictly richer than OpenAI's documented range (minimal/low/medium/high):
	// at least two tiers above high.
	if EffortRank(EffortXHigh) <= EffortRank(EffortHigh) || EffortRank(EffortMax) <= EffortRank(EffortXHigh) {
		t.Fatal("xhigh/max must rank above high")
	}
	if ValidEffort("nope") || !ValidEffort(EffortMax) {
		t.Fatal("ValidEffort broken")
	}
	if EffortRank("nope") != -1 {
		t.Fatal("unknown tier must rank -1")
	}
}
