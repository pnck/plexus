package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *CheckpointStore {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cs, err := NewCheckpointStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}
	return cs
}

func TestAppendAssignsSequentialSeq(t *testing.T) {
	ctx := context.Background()
	cs := newStore(t)

	for i := int64(0); i < 3; i++ {
		cp, err := cs.Append(ctx, "task-1", "goal")
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if cp.Seq != i {
			t.Fatalf("Append seq = %d, want %d", cp.Seq, i)
		}
		if cp.Status != Pending {
			t.Fatalf("new checkpoint status = %v, want Pending", cp.Status)
		}
	}
	// A different task starts its own chain at 0.
	other, err := cs.Append(ctx, "task-2", "goal")
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if other.Seq != 0 {
		t.Fatalf("task-2 first seq = %d, want 0", other.Seq)
	}
}

func TestLifecycleHappyPath(t *testing.T) {
	ctx := context.Background()
	cs := newStore(t)

	cp, err := cs.Append(ctx, "t", "ship the thing")
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := cs.Activate(ctx, "t", cp.Seq); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	got, err := cs.Get(ctx, "t", cp.Seq)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != Active {
		t.Fatalf("status = %v, want Active", got.Status)
	}
	if err := cs.Complete(ctx, "t", cp.Seq, "shipped"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got, err = cs.Get(ctx, "t", cp.Seq)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != Done || got.Result != "shipped" {
		t.Fatalf("after Complete: status=%v result=%q, want Done/\"shipped\"", got.Status, got.Result)
	}
}

func TestStepListOrderAndActive(t *testing.T) {
	ctx := context.Background()
	cs := newStore(t)

	goals := []string{"plan", "build", "verify"}
	for _, g := range goals {
		if _, err := cs.Append(ctx, "t", g); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	steps, err := cs.Steps(ctx, "t")
	if err != nil {
		t.Fatalf("Steps: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("len(steps) = %d, want 3", len(steps))
	}
	for i, g := range goals {
		if steps[i].Goal != g || steps[i].Seq != int64(i) {
			t.Fatalf("step %d = {seq:%d goal:%q}, want {seq:%d goal:%q}", i, steps[i].Seq, steps[i].Goal, i, g)
		}
	}

	// No active step yet.
	if _, ok, err := cs.Active(ctx, "t"); err != nil || ok {
		t.Fatalf("Active before any activation: ok=%v err=%v, want false/nil", ok, err)
	}
	// Activate the middle step; Active() returns exactly it.
	if err := cs.Activate(ctx, "t", 1); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	active, ok, err := cs.Active(ctx, "t")
	if err != nil || !ok {
		t.Fatalf("Active: ok=%v err=%v, want true/nil", ok, err)
	}
	if active.Seq != 1 || active.Goal != "build" {
		t.Fatalf("Active = {seq:%d goal:%q}, want {1 build}", active.Seq, active.Goal)
	}
}

func TestSuspendResumeByCorrelation(t *testing.T) {
	ctx := context.Background()
	cs := newStore(t)

	// Two tasks each reach an Active step, then suspend waiting on answers.
	for _, task := range []string{"a", "b"} {
		cp, err := cs.Append(ctx, task, "ask the human")
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := cs.Activate(ctx, task, cp.Seq); err != nil {
			t.Fatalf("Activate: %v", err)
		}
	}
	if err := cs.Suspend(ctx, "a", 0, "corr-X"); err != nil {
		t.Fatalf("Suspend a: %v", err)
	}
	if err := cs.Suspend(ctx, "b", 0, "corr-Y"); err != nil {
		t.Fatalf("Suspend b: %v", err)
	}

	// The answer for corr-X arrives: only task a's step is waiting on it.
	waiters, err := cs.Waiting(ctx, "corr-X")
	if err != nil {
		t.Fatalf("Waiting: %v", err)
	}
	if len(waiters) != 1 || waiters[0].TaskID != "a" {
		t.Fatalf("Waiting(corr-X) = %+v, want one waiter on task a", waiters)
	}

	// Wake it: Suspended -> Active, WaitFor cleared.
	if err := cs.Activate(ctx, "a", 0); err != nil {
		t.Fatalf("Activate (wake): %v", err)
	}
	woken, err := cs.Get(ctx, "a", 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if woken.Status != Active || woken.WaitFor != "" {
		t.Fatalf("woken = {status:%v waitFor:%q}, want Active/\"\"", woken.Status, woken.WaitFor)
	}
	// corr-X now has no waiters; task b still parked on corr-Y.
	if w, _ := cs.Waiting(ctx, "corr-X"); len(w) != 0 {
		t.Fatalf("Waiting(corr-X) after wake = %d, want 0", len(w))
	}
	if w, _ := cs.Waiting(ctx, "corr-Y"); len(w) != 1 {
		t.Fatalf("Waiting(corr-Y) = %d, want 1", len(w))
	}
}

func TestTransitionGuards(t *testing.T) {
	ctx := context.Background()
	cs := newStore(t)
	if _, err := cs.Append(ctx, "t", "g"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Complete requires Active; the step is still Pending -> ErrConflict.
	if err := cs.Complete(ctx, "t", 0, "r"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Complete on Pending: err=%v, want ErrConflict", err)
	}
	// Unknown row -> ErrNotFound.
	if err := cs.Activate(ctx, "t", 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Activate missing: err=%v, want ErrNotFound", err)
	}
	if _, err := cs.Get(ctx, "t", 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: err=%v, want ErrNotFound", err)
	}
	// Suspend without a correlation id is rejected outright.
	if err := cs.Activate(ctx, "t", 0); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := cs.Suspend(ctx, "t", 0, ""); err == nil {
		t.Fatal("Suspend with empty WaitFor: want error, got nil")
	}
}

// TestResumeSurvivesReopen exercises the durability contract behind §5.7.5: the
// process can die mid-yield and the checkpoint chain is recovered from disk.
func TestResumeSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ckpt.db")

	// First "process": build a chain, finish step 0, suspend step 1.
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cs1, err := NewCheckpointStore(ctx, db1)
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}
	if _, err := cs1.Append(ctx, "t", "plan"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := cs1.Append(ctx, "t", "execute"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_ = cs1.Activate(ctx, "t", 0)
	_ = cs1.Complete(ctx, "t", 0, "planned")
	_ = cs1.Activate(ctx, "t", 1)
	if err := cs1.Suspend(ctx, "t", 1, "corr-Z"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	_ = db1.Close() // process dies

	// Second "process": reopen and recover.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	cs2, err := NewCheckpointStore(ctx, db2)
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}

	steps, err := cs2.Steps(ctx, "t")
	if err != nil {
		t.Fatalf("Steps: %v", err)
	}
	if len(steps) != 2 || steps[0].Status != Done || steps[0].Result != "planned" {
		t.Fatalf("recovered step0 = %+v, want Done/planned", steps[0])
	}
	if steps[1].Status != Suspended || steps[1].WaitFor != "corr-Z" {
		t.Fatalf("recovered step1 = %+v, want Suspended/corr-Z", steps[1])
	}
	// The pending answer can still find its waiter and wake it.
	waiters, err := cs2.Waiting(ctx, "corr-Z")
	if err != nil || len(waiters) != 1 {
		t.Fatalf("Waiting after reopen: %+v err=%v, want one waiter", waiters, err)
	}
}
