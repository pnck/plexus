package store

import (
	"context"
	"errors"
	"testing"
)

func newWorkingMemory(t *testing.T) *WorkingMemoryStore {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	wm, err := NewWorkingMemoryStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewWorkingMemoryStore: %v", err)
	}
	return wm
}

func TestWorkingMemoryPutGetUpsert(t *testing.T) {
	ctx := context.Background()
	wm := newWorkingMemory(t)

	if err := wm.Put(ctx, "t", "constraint", "no network calls", Manual); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := wm.Get(ctx, "t", "constraint")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "no network calls" || got.Source != Manual {
		t.Fatalf("note = %+v, want content/Manual", got)
	}

	// A second Put to the same key overwrites (upsert), including source.
	if err := wm.Put(ctx, "t", "constraint", "no network; prefer goroutines", Compact); err != nil {
		t.Fatalf("Put (upsert): %v", err)
	}
	got, err = wm.Get(ctx, "t", "constraint")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "no network; prefer goroutines" || got.Source != Compact {
		t.Fatalf("after upsert note = %+v, want updated content/Compact", got)
	}
}

func TestWorkingMemoryScopeIsolationAndList(t *testing.T) {
	ctx := context.Background()
	wm := newWorkingMemory(t)

	_ = wm.Put(ctx, "t1", "b", "two", Manual)
	_ = wm.Put(ctx, "t1", "a", "one", Manual)
	_ = wm.Put(ctx, "t2", "a", "other", Manual)

	list, err := wm.List(ctx, "t1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Key != "a" || list[1].Key != "b" {
		t.Fatalf("List(t1) = %+v, want [a, b] ordered", list)
	}
	// Scope isolation: t2's key "a" is independent.
	other, err := wm.Get(ctx, "t2", "a")
	if err != nil || other.Content != "other" {
		t.Fatalf("Get(t2,a) = %+v err=%v, want content \"other\"", other, err)
	}
}

func TestWorkingMemoryGetMissingAndDelete(t *testing.T) {
	ctx := context.Background()
	wm := newWorkingMemory(t)

	if _, err := wm.Get(ctx, "t", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: err=%v, want ErrNotFound", err)
	}
	// Delete of an absent note is a no-op, not an error.
	if err := wm.Delete(ctx, "t", "nope"); err != nil {
		t.Fatalf("Delete absent: %v", err)
	}
	_ = wm.Put(ctx, "t", "k", "v", Manual)
	if err := wm.Delete(ctx, "t", "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := wm.Get(ctx, "t", "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: err=%v, want ErrNotFound", err)
	}
	// Empty scope/key is rejected.
	if err := wm.Put(ctx, "", "k", "v", Manual); err == nil {
		t.Fatal("Put with empty scope: want error")
	}
}
