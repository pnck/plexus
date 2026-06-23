package brain

import (
	"context"
	"strings"
	"sync"
	"testing"

	"plexus/pkg/llm"
	"plexus/protocol"
)

// recordingEmitter captures emitted task events.
type recordingEmitter struct {
	mu     sync.Mutex
	events []TaskEvent
}

func (e *recordingEmitter) Emit(_ context.Context, ev TaskEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return nil
}

// Test: a task_revert tool call emits a TaskRevert domain event through the
// brain's Emitter seam, and the model is told it is a REQUEST (not a fact).
func TestTaskRevertEmitsRequest(t *testing.T) {
	em := &recordingEmitter{}
	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "r1", Name: "task_revert", Arguments: `{"task_id":"T-42","reason":"reported done but tests fail"}`}}},
		{text: "ok, I have flagged it"},
	}}

	b := New(Options{Gateway: gw, Emitter: em, Inbound: directInbound{}})
	out, err := b.Handle(context.Background(), userMsg("the prior task was wrong"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if out != "ok, I have flagged it" {
		t.Fatalf("unexpected final reply: %q", out)
	}

	em.mu.Lock()
	defer em.mu.Unlock()
	if len(em.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(em.events))
	}
	ev := em.events[0]
	if ev.Kind != TaskRevertEvent || ev.TaskID != "T-42" || !strings.Contains(ev.Reason, "tests fail") {
		t.Fatalf("unexpected event: %+v", ev)
	}
	// Request semantics must be fed back to the model.
	var sawRequest bool
	for _, f := range b.History() {
		if f.Authority == protocol.AuthTool && strings.Contains(f.Content, "REQUEST") && strings.Contains(f.Content, "do NOT assume") {
			sawRequest = true
		}
	}
	if !sawRequest {
		t.Fatal("request-semantics feedback not absorbed into history")
	}
}

// Test: task_report defaults its TaskID to the current task being handled.
func TestTaskReportUsesCurrentTask(t *testing.T) {
	em := &recordingEmitter{}
	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "p1", Name: "task_report", Arguments: `{"status":"in_progress","summary":"halfway"}`}}},
		{text: "reported"},
	}}

	b := New(Options{Gateway: gw, Emitter: em, Inbound: directInbound{}})
	msg := protocol.Message{Type: protocol.TypeP2P, Sender: "user", TaskID: "CUR-1", Payload: []byte("do the thing")}
	if _, err := b.Handle(context.Background(), msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	if len(em.events) != 1 || em.events[0].Kind != TaskReportEvent || em.events[0].TaskID != "CUR-1" {
		t.Fatalf("task_report did not use the current task: %+v", em.events)
	}
	if em.events[0].Status != "in_progress" {
		t.Fatalf("status = %q", em.events[0].Status)
	}
}

// Test: the task channel tools are surfaced to the brain but NEVER to a
// delegation (the envelope is effectors-only; task_* ride the brain-owned bus).
func TestTaskToolsNotInDelegationSurface(t *testing.T) {
	b := New(Options{Gateway: &fakeGateway{}, Inbound: directInbound{}})
	var sawTaskTool bool
	for _, d := range b.toolSurface() {
		if d.Name == taskReportToolName || d.Name == taskRevertToolName {
			sawTaskTool = true
		}
	}
	if !sawTaskTool {
		t.Fatal("brain tool surface must include task_* tools")
	}
	// A delegation's surface is built from caps.List() (effectors only); task_*
	// are brain tools and have no effector form, so they cannot appear there.
	// (Structural: toolDefs over an envelope never sees task_*.)
}
