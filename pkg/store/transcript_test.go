package store

import (
	"context"
	"testing"
)

func newArchive(t *testing.T) *TranscriptArchiveStore {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ar, err := NewTranscriptArchiveStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewTranscriptArchiveStore: %v", err)
	}
	return ar
}

func TestArchiveAppendAssignsSeq(t *testing.T) {
	ctx := context.Background()
	ar := newArchive(t)

	r0, err := ar.Append(ctx, "t", CompactedFrame, "", "old frame")
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	r1, err := ar.Append(ctx, "t", DelegationTranscript, "deleg-7", "full transcript")
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if r0.Seq != 0 || r1.Seq != 1 {
		t.Fatalf("seqs = %d,%d, want 0,1", r0.Seq, r1.Seq)
	}
	if r1.Kind != DelegationTranscript || r1.Ref != "deleg-7" {
		t.Fatalf("r1 = %+v, want DelegationTranscript/deleg-7", r1)
	}
}

func TestArchiveProgressiveListAndDrop(t *testing.T) {
	ctx := context.Background()
	ar := newArchive(t)

	for i := 0; i < 5; i++ {
		if _, err := ar.Append(ctx, "t", CompactedFrame, "", "frame"); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// Progressive retrieval: a window from seq 2, capped at 2.
	window, err := ar.List(ctx, "t", 2, 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(window) != 2 || window[0].Seq != 2 || window[1].Seq != 3 {
		t.Fatalf("window = %+v, want seqs [2,3]", window)
	}
	// Full read (no cap).
	all, err := ar.List(ctx, "t", 0, 0)
	if err != nil || len(all) != 5 {
		t.Fatalf("List all = %d err=%v, want 5", len(all), err)
	}
	// Droppable: discard the scope's cold records.
	n, err := ar.Drop(ctx, "t")
	if err != nil || n != 5 {
		t.Fatalf("Drop = %d err=%v, want 5", n, err)
	}
	if remaining, _ := ar.List(ctx, "t", 0, 0); len(remaining) != 0 {
		t.Fatalf("after Drop = %d records, want 0", len(remaining))
	}
}
