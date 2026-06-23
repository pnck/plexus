package anthropic

import (
	"testing"

	"plexus/pkg/llm"
)

// The unified effort level maps to an extended-thinking token budget (≥1024),
// and WithReasoningEffort flows it to the provider; an unknown/empty level
// disables thinking (budget 0).
func TestReasoningEffortToThinkingBudget(t *testing.T) {
	cases := map[string]int64{
		"minimal": 1024, "low": 2048, "medium": 4096, "high": 8192,
		"xhigh": 16384, "max": 32768, "": 0, "bogus": 0,
	}
	for level, want := range cases {
		if got := thinkingBudgetFor(level); got != want {
			t.Errorf("thinkingBudgetFor(%q) = %d, want %d", level, got, want)
		}
	}
	if p := NewProvider("k", "claude", WithReasoningEffort("xhigh")); p.thinkingBudget != 16384 {
		t.Fatalf("thinkingBudget = %d, want 16384", p.thinkingBudget)
	}
	if p := NewProvider("k", "claude"); p.thinkingBudget != 0 {
		t.Fatalf("default thinkingBudget = %d, want 0", p.thinkingBudget)
	}
	// Every enabled tier must satisfy the API minimum (1024) and increase
	// monotonically (Anthropic honors the high tiers instead of clamping).
	prev := int64(0)
	for _, level := range llm.ReasoningEfforts {
		b := thinkingBudgetFor(level)
		if b < 1024 {
			t.Errorf("budget for %q = %d, below the 1024 minimum", level, b)
		}
		if b <= prev {
			t.Errorf("budget for %q = %d not greater than previous %d", level, b, prev)
		}
		prev = b
	}
}
