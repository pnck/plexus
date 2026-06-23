package brain

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"plexus/pkg/effector"
	"plexus/pkg/llm"
	"plexus/pkg/store"
	"plexus/protocol"
)

// taskMsg is a user message scoped to a task — a yield must Suspend a step, which
// requires a non-empty TaskID (an empty task id has no checkpoint chain).
func taskMsg(task, text string) protocol.Message {
	return protocol.Message{Type: protocol.TypeP2P, Sender: "user", TaskID: task, Payload: []byte(text)}
}

// gatedReg returns a registry with a single approval-gated effector (run_command,
// ExecArbitrary) whose Invoke records and returns a fixed result (no real exec).
func gatedReg(called *bool) *effector.Registry {
	reg := effector.NewRegistry(nil)
	reg.Register(recordingEffector{name: "run_command", risk: effector.ExecArbitrary, out: "EXEC-OK", called: called})
	return reg
}

func openCheckpoints(t *testing.T, path string) *store.CheckpointStore {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cps, err := store.NewCheckpointStore(context.Background(), db)
	if err != nil {
		t.Fatalf("checkpoint store: %v", err)
	}
	return cps
}

// constCorr makes the yield correlation id deterministic for assertions.
func constCorr(id string) func() string { return func() string { return id } }

// TestYieldPreciseResumeGranted: a gated call suspends a step and returns a
// YieldError; resuming the SAME (live) brain with granted=true runs the effector
// and finishes the turn — the precise in-process path.
func TestYieldPreciseResumeGranted(t *testing.T) {
	called := false
	reg := gatedReg(&called)
	cps := openCheckpoints(t, ":memory:")

	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "c1", Name: "run_command", Arguments: `{"command":"deploy"}`}}},
		{text: "ran it"},
	}}
	b := New(Options{
		Gateway: gw, Registry: reg, Checkpoints: cps,
		YieldForApproval: true, NewCorrID: constCorr("corr-1"),
	})

	_, err := b.Handle(context.Background(), taskMsg("task-1", "deploy please"))
	ye, ok := AsYield(err)
	if !ok {
		t.Fatalf("expected a YieldError, got %v", err)
	}
	if ye.Corr != "corr-1" || !strings.Contains(ye.Description, "run_command") {
		t.Fatalf("yield = %+v", ye)
	}
	if called {
		t.Fatal("gated effector ran BEFORE approval")
	}
	// The step is persisted as Suspended waiting on the corr.
	waiters, _ := cps.Waiting(context.Background(), "corr-1")
	if len(waiters) != 1 {
		t.Fatalf("want 1 suspended waiter, got %d", len(waiters))
	}

	out, err := b.Resume(context.Background(), "corr-1", true)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if !called {
		t.Fatal("gated effector did not run after approval")
	}
	if out != "ran it" {
		t.Fatalf("resumed reply = %q", out)
	}
}

// TestYieldPreciseResumeDenied: resuming with granted=false feeds the standing
// denial back to the model and never runs the effector.
func TestYieldPreciseResumeDenied(t *testing.T) {
	called := false
	reg := gatedReg(&called)
	cps := openCheckpoints(t, ":memory:")

	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "c1", Name: "run_command", Arguments: `{"command":"rm -rf"}`}}},
		{text: "won't run it"},
	}}
	b := New(Options{
		Gateway: gw, Registry: reg, Checkpoints: cps,
		YieldForApproval: true, NewCorrID: constCorr("corr-1"),
	})

	if _, err := b.Handle(context.Background(), taskMsg("task-1", "delete everything")); err == nil {
		t.Fatal("expected a yield")
	}
	out, err := b.Resume(context.Background(), "corr-1", false)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if called {
		t.Fatal("gated effector ran despite denial")
	}
	if out != "won't run it" {
		t.Fatalf("resumed reply = %q", out)
	}
	var sawDenied bool
	for _, f := range b.History() {
		if strings.Contains(f.Content, "DENIED") {
			sawDenied = true
		}
	}
	if !sawDenied {
		t.Fatal("denial not fed back to the model")
	}
}

// TestYieldFreshBrainRebuildResume is the cross-process resume (§5.7.5/§5.7.9): the
// brain that suspended "dies"; a FRESH brain over the SAME file-backed checkpoint
// store (its in-memory transcript gone) wakes the suspended step, rebuilds working
// context from the persisted step chain, and continues — never replaying history.
func TestYieldFreshBrainRebuildResume(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "brain.db")

	// First incarnation: suspends on a gated call, then "dies".
	cps1 := openCheckpoints(t, dbPath)
	gw1 := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "c1", Name: "run_command", Arguments: `{"command":"deploy"}`}}},
	}}
	b1 := New(Options{
		Gateway: gw1, Registry: gatedReg(nil), Checkpoints: cps1,
		YieldForApproval: true, NewCorrID: constCorr("corr-1"),
	})
	if _, err := b1.Handle(context.Background(), taskMsg("task-1", "deploy please")); err == nil {
		t.Fatal("expected b1 to yield")
	}
	// b1 is discarded (process death) — its in-memory history is gone.

	// Second incarnation: a fresh brain over the same persisted store. It has no
	// `pending` for corr-1, so Resume takes the rebuild-from-step-chain path.
	called := false
	cps2 := openCheckpoints(t, dbPath)
	gw2 := &fakeGateway{turns: []scriptedTurn{
		{text: "resumed and completed the deploy"},
	}}
	b2 := New(Options{
		Gateway: gw2, Registry: gatedReg(&called), Checkpoints: cps2,
		YieldForApproval: true, NewCorrID: constCorr("corr-2"),
	})

	out, err := b2.Resume(context.Background(), "corr-1", true)
	if err != nil {
		t.Fatalf("fresh Resume: %v", err)
	}
	if out != "resumed and completed the deploy" {
		t.Fatalf("fresh resume reply = %q", out)
	}
	// The fresh brain rebuilt context from the step chain: its first (only) LLM call
	// carried the resume briefing with the human's GRANTED decision, NOT a replay of
	// the dead transcript.
	gw2.mu.Lock()
	defer gw2.mu.Unlock()
	if len(gw2.calls) == 0 {
		t.Fatal("fresh brain never called the gateway")
	}
	var briefing string
	for _, m := range gw2.calls[0] {
		if m.Role == llm.RoleUser {
			briefing = m.Content
		}
	}
	if !strings.Contains(briefing, "GRANTED") || !strings.Contains(briefing, "run_command") {
		t.Fatalf("resume briefing missing decision/step context: %q", briefing)
	}
	// The suspended step is no longer waiting (it was activated on resume).
	waiters, _ := cps2.Waiting(context.Background(), "corr-1")
	if len(waiters) != 0 {
		t.Fatalf("step still suspended after resume: %d waiters", len(waiters))
	}
}
