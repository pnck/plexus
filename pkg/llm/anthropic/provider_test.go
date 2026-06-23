package anthropic

import (
	"encoding/json"
	"testing"

	"plexus/pkg/llm"
)

// An assistant turn that called a tool must serialize with the full content-block
// structure Anthropic requires: any signed thinking block FIRST (extended-thinking
// replay rule), then text, then the tool_use block; the tool result becomes a
// tool_result block. The old "simplified" mapping dropped the tool_use entirely.
func TestToAnthropicMessagesToolUseAndThinkingReplay(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "you are a bot"},
		{Role: llm.RoleUser, Content: "do it"},
		{
			Role:      llm.RoleAssistant,
			Content:   "let me check",
			ToolCalls: []llm.ToolCall{{ID: "t1", Name: "read_file", Arguments: `{"path":"/x"}`}},
			Reasoning: []llm.ReasoningBlock{{Text: "I should read the file", Signature: "sig-abc"}},
		},
		{Role: llm.RoleTool, ToolCallID: "t1", Content: "file contents"},
	}

	out, system := toAnthropicMessages(msgs)

	if len(system) != 1 || system[0].Text != "you are a bot" {
		t.Fatalf("system blocks = %+v", system)
	}
	if len(out) != 3 { // user, assistant, user(tool_result)
		t.Fatalf("message count = %d, want 3", len(out))
	}

	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)

	// The tool_use block (and its input) must be present — the old mapping lost it.
	for _, want := range []string{"tool_use", "read_file", `"path":"/x"`, "tool_result", "file contents", "sig-abc"} {
		if !contains(s, want) {
			t.Fatalf("serialized messages missing %q: %s", want, s)
		}
	}
	// Ordering inside the assistant turn: thinking (signature) precedes the
	// tool_use it produced.
	if idx(s, "sig-abc") > idx(s, "tool_use") {
		t.Fatalf("thinking block must precede tool_use: %s", s)
	}
}

func contains(s, sub string) bool { return idx(s, sub) >= 0 }
func idx(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

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
