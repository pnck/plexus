package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"plexus/pkg/llm"
)

// A tool result must serialize with tool_call_id = the call id and content = the
// result text. Regression for the swapped ToolMessage(content, toolCallID) args
// that produced OpenAI 400 "tool_call_id ... not found in tool_calls".
func TestToOpenAIMessagesToolResultPairing(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_abc", Name: "step_add", Arguments: `{"goal":"x"}`}}},
		{Role: llm.RoleTool, ToolCallID: "call_abc", Content: "added step #0: x"},
	}
	out := toOpenAIMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("got %d messages, want 2", len(out))
	}

	// Marshal the tool message (request param) and inspect the wire fields.
	b, err := json.Marshal(out[1])
	if err != nil {
		t.Fatalf("marshal tool message: %v", err)
	}
	var got struct {
		Role       string `json:"role"`
		ToolCallID string `json:"tool_call_id"`
		Content    string `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, b)
	}
	if got.Role != "tool" {
		t.Fatalf("role = %q, want tool (raw: %s)", got.Role, b)
	}
	if got.ToolCallID != "call_abc" {
		t.Fatalf("tool_call_id = %q, want call_abc — args swapped? (raw: %s)", got.ToolCallID, b)
	}
	if !strings.Contains(string(b), "added step #0: x") {
		t.Fatalf("content not carried: %s", b)
	}

	// The assistant tool-call turn must carry the matching call id.
	ab, _ := json.Marshal(out[0])
	if !strings.Contains(string(ab), "call_abc") {
		t.Fatalf("assistant tool_calls missing call id: %s", ab)
	}
}
