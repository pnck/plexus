package effector

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"plexus/pkg/store"
)

func newCheckpoints(t *testing.T) *store.CheckpointStore {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cs, err := store.NewCheckpointStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}
	return cs
}

func TestStepPrimitivesPlanFlow(t *testing.T) {
	cs := newCheckpoints(t)
	ctx := WithTaskScope(context.Background(), "task-9")
	add, start, complete, list := StepAdd(cs), StepStart(cs), StepComplete(cs), StepList(cs)

	// Build a two-step plan.
	if r, _ := add.Invoke(ctx, json.RawMessage(`{"goal":"plan"}`)); r.IsError || !strings.Contains(r.Content, "#0") {
		t.Fatalf("step_add #0 = %q isErr=%v", r.Content, r.IsError)
	}
	if r, _ := add.Invoke(ctx, json.RawMessage(`{"goal":"build"}`)); !strings.Contains(r.Content, "#1") {
		t.Fatalf("step_add #1 = %q", r.Content)
	}
	// Start then complete step 0.
	if r, _ := start.Invoke(ctx, json.RawMessage(`{"seq":0}`)); r.IsError {
		t.Fatalf("step_start: %s", r.Content)
	}
	if r, _ := complete.Invoke(ctx, json.RawMessage(`{"seq":0,"result":"planned"}`)); r.IsError {
		t.Fatalf("step_complete: %s", r.Content)
	}
	// list reflects status + result.
	r, _ := list.Invoke(ctx, json.RawMessage(`{}`))
	if !strings.Contains(r.Content, "#0 [Done] plan — planned") || !strings.Contains(r.Content, "#1 [Pending] build") {
		t.Fatalf("step_list = %q", r.Content)
	}
}

func TestStepPrimitivesGuardsAndScope(t *testing.T) {
	cs := newCheckpoints(t)
	ctx := WithTaskScope(context.Background(), "t")
	add, complete, start := StepAdd(cs), StepComplete(cs), StepStart(cs)

	if _, err := add.Invoke(ctx, json.RawMessage(`{"goal":"g"}`)); err != nil {
		t.Fatalf("step_add: %v", err)
	}
	// complete a Pending (not Active) step -> conflict feedback.
	if r, _ := complete.Invoke(ctx, json.RawMessage(`{"seq":0}`)); !r.IsError || !strings.Contains(r.Content, "not in a state") {
		t.Fatalf("step_complete on Pending = %q isErr=%v, want conflict", r.Content, r.IsError)
	}
	// unknown step -> not found.
	if r, _ := start.Invoke(ctx, json.RawMessage(`{"seq":42}`)); !r.IsError || !strings.Contains(r.Content, "no step #42") {
		t.Fatalf("step_start missing = %q", r.Content)
	}
	// scope isolation: another task's plan is empty.
	other := WithTaskScope(context.Background(), "other")
	if r, _ := StepList(cs).Invoke(other, json.RawMessage(`{}`)); !strings.Contains(r.Content, "no steps yet") {
		t.Fatalf("cross-scope step_list = %q, want empty", r.Content)
	}
}

func TestStepAndMemoryAreAgentPrivate(t *testing.T) {
	wm := newWM(t)
	cs := newCheckpoints(t)
	r := NewRegistry(nil)
	RegisterBuiltins(r, BuiltinOptions{WorkingMemory: wm, Checkpoints: cs})

	inEnvelope := map[string]bool{}
	for _, e := range r.DelegationEnvelope().List() {
		inEnvelope[e.Name()] = true
	}
	for _, private := range []string{"step_add", "step_start", "step_complete", "step_block", "step_list", "mem_read", "mem_write"} {
		if inEnvelope[private] {
			t.Errorf("agent-private %q leaked into delegation envelope", private)
		}
		if _, ok := r.Get(private); !ok {
			t.Errorf("expected %q registered", private)
		}
	}
	// file primitives remain shareable.
	if !inEnvelope["read_file"] {
		t.Error("read_file should be inside the envelope")
	}
}
