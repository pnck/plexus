package chat

import (
	"context"
	"strings"
	"sync"
	"testing"

	"plexus/pkg/llm"
)

// ---- minimal scripted fake gateway (brain + delegation scripts) ----

type scriptedTurn struct {
	text  string
	calls []llm.ToolCall
}

type fakeStream struct {
	events []llm.StreamEvent
	idx    int
}

func (s *fakeStream) Next() bool               { s.idx++; return s.idx <= len(s.events) }
func (s *fakeStream) Current() llm.StreamEvent { return s.events[s.idx-1] }
func (s *fakeStream) Err() error               { return nil }
func (s *fakeStream) Close() error             { return nil }

type fakeGateway struct {
	mu          sync.Mutex
	turns, dele []scriptedTurn
	bi, di      int
}

func (g *fakeGateway) GenerateStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDefinition) (llm.EventStream, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	isDeleg := len(msgs) > 0 && msgs[0].Role == llm.RoleSystem && strings.Contains(msgs[0].Content, "isolated sub-cognition")
	var t scriptedTurn
	if isDeleg {
		t = g.dele[g.di]
		g.di++
	} else {
		t = g.turns[g.bi]
		g.bi++
	}
	var evs []llm.StreamEvent
	if t.text != "" {
		evs = append(evs, llm.StreamEvent{DeltaText: t.text})
	}
	for i := range t.calls {
		c := t.calls[i]
		evs = append(evs, llm.StreamEvent{ToolCall: &c})
	}
	evs = append(evs, llm.StreamEvent{FinishReason: "stop"})
	return &fakeStream{events: evs}, nil
}

func newAgent(t *testing.T, gw llm.Provider) *Agent {
	t.Helper()
	a, err := New(context.Background(), Config{Gateway: gw, DBPath: ":memory:"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// The assembled agent drives a real effector (step_add) against the real
// brain-private CheckpointStore, then converges.
func TestAssembledAgentRunsEffector(t *testing.T) {
	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "s1", Name: "step_add", Arguments: `{"goal":"help the user"}`}}},
		{text: "I've noted a step and I'm ready to help."},
	}}
	a := newAgent(t, gw)

	out, err := a.Handle(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if out != "I've noted a step and I'm ready to help." {
		t.Fatalf("reply = %q", out)
	}
	// The step_add result is only present if the real CheckpointStore write
	// succeeded under the standing chat task scope.
	var sawStep bool
	for _, f := range a.Brain.History() {
		if strings.Contains(f.Content, "added step #0") {
			sawStep = true
		}
	}
	if !sawStep {
		t.Fatal("step_add did not run against the real checkpoint store")
	}
}

// L1 is seeded with the kernel principles first, then the chat role card (the
// standing pseudo-task framing).
func TestAssembledAgentSeedsKernelThenRoleCard(t *testing.T) {
	a := newAgent(t, &fakeGateway{})
	h := a.Brain.History()
	if len(h) != 2 || h[0].Provenance != "principles" || h[1].Provenance != "role_card" {
		t.Fatalf("L1 frames = %+v, want [principles, role_card]", h)
	}
	if !strings.Contains(h[1].Content, "open-ended") {
		t.Fatalf("chat role card missing pseudo-task framing: %q", h[1].Content)
	}
}

// task_revert is rejected in chat (open-ended pseudo-task) and the rejection is
// fed back to the model.
func TestAssembledAgentRejectsTaskRevert(t *testing.T) {
	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "r1", Name: "task_revert", Arguments: `{"task_id":"chat","reason":"x"}`}}},
		{text: "understood"},
	}}
	a := newAgent(t, gw)

	if _, err := a.Handle(context.Background(), "revert please"); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var sawReject bool
	for _, f := range a.Brain.History() {
		if strings.Contains(f.Content, "not available in chat") {
			sawReject = true
		}
	}
	if !sawReject {
		t.Fatal("task_revert was not rejected")
	}
}

// The agent can delegate a sub-task; the delegation's distilled Result is absorbed.
func TestAssembledAgentDelegates(t *testing.T) {
	gw := &fakeGateway{
		turns: []scriptedTurn{
			{calls: []llm.ToolCall{{ID: "d1", Name: "delegate", Arguments: `{"objective":"survey"}`}}},
			{text: "done"},
		},
		dele: []scriptedTurn{
			{text: `{"summary":"surveyed the repo"}`},
		},
	}
	a := newAgent(t, gw)

	out, err := a.Handle(context.Background(), "survey the repo")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if out != "done" {
		t.Fatalf("reply = %q", out)
	}
	var sawDistilled bool
	for _, f := range a.Brain.History() {
		if strings.Contains(f.Content, "surveyed the repo") {
			sawDistilled = true
		}
	}
	if !sawDistilled {
		t.Fatal("delegation result not absorbed")
	}
}
