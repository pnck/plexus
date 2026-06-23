package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ArchiveKind distinguishes the two cold-record sources unified by
// TranscriptArchive (§5.7.9): old frames evicted by compaction, and the full
// transcript of a finished delegation. They are the same thing — bulky raw
// records kept only for audit/retrieval, never replayed into the brain.
type ArchiveKind int

const (
	CompactedFrame       ArchiveKind = iota // an old frame evicted by compaction
	DelegationTranscript                    // the full transcript of a delegation
)

func (k ArchiveKind) String() string {
	switch k {
	case CompactedFrame:
		return "compacted_frame"
	case DelegationTranscript:
		return "delegation_transcript"
	default:
		return fmt.Sprintf("ArchiveKind(%d)", int(k))
	}
}

// ArchiveRecord is one cold record. Ref is an optional pointer back to the
// source (e.g. a delegation label or trace id) for retrieval; it may be empty.
type ArchiveRecord struct {
	Scope   string // owning scope (task id)
	Seq     int64  // append order within the scope
	Kind    ArchiveKind
	Ref     string
	Content string
}

const transcriptArchiveSchema = `
CREATE TABLE IF NOT EXISTS transcript_archive (
    scope   TEXT    NOT NULL,
    seq     INTEGER NOT NULL,
    kind    INTEGER NOT NULL,
    ref     TEXT    NOT NULL DEFAULT '',
    content TEXT    NOT NULL,
    PRIMARY KEY (scope, seq)
);
`

// TranscriptArchiveStore persists TranscriptArchive (§5.7.9): an append-only,
// droppable log of cold records over its own *sql.DB. It NEVER feeds the brain
// context (§5.7.7 invariant) — it exists for observability and progressive
// retrieval, and a scope's records can be dropped once their summary is
// confirmed sufficient.
type TranscriptArchiveStore struct {
	db *sql.DB
}

// NewTranscriptArchiveStore migrates the schema on db and returns a store.
func NewTranscriptArchiveStore(ctx context.Context, db *sql.DB) (*TranscriptArchiveStore, error) {
	if _, err := db.ExecContext(ctx, transcriptArchiveSchema); err != nil {
		return nil, fmt.Errorf("store: migrate transcript_archive: %w", err)
	}
	return &TranscriptArchiveStore{db: db}, nil
}

// Append adds a cold record to the end of a scope's archive and returns it. Seq
// is assigned atomically as max(seq)+1 (first record is Seq 0); the computation
// and insert share a transaction so concurrent appends cannot collide.
func (s *TranscriptArchiveStore) Append(ctx context.Context, scope string, kind ArchiveKind, ref, content string) (ArchiveRecord, error) {
	if scope == "" {
		return ArchiveRecord{}, errors.New("store: empty archive scope")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArchiveRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq)+1, 0) FROM transcript_archive WHERE scope = ?`, scope,
	).Scan(&seq); err != nil {
		return ArchiveRecord{}, fmt.Errorf("store: next archive seq: %w", err)
	}
	rec := ArchiveRecord{Scope: scope, Seq: seq, Kind: kind, Ref: ref, Content: content}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO transcript_archive (scope, seq, kind, ref, content) VALUES (?, ?, ?, ?, ?)`,
		rec.Scope, rec.Seq, int(rec.Kind), rec.Ref, rec.Content,
	); err != nil {
		return ArchiveRecord{}, fmt.Errorf("store: insert archive record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ArchiveRecord{}, err
	}
	return rec, nil
}

// List returns a scope's records with Seq >= fromSeq in order, capped at limit
// (limit <= 0 means no cap). This backs progressive retrieval: read a window,
// confirm the summary suffices, then drop.
func (s *TranscriptArchiveStore) List(ctx context.Context, scope string, fromSeq int64, limit int) ([]ArchiveRecord, error) {
	query := `SELECT scope, seq, kind, ref, content FROM transcript_archive WHERE scope = ? AND seq >= ? ORDER BY seq`
	args := []any{scope, fromSeq}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ArchiveRecord
	for rows.Next() {
		var rec ArchiveRecord
		var kind int
		if err := rows.Scan(&rec.Scope, &rec.Seq, &kind, &rec.Ref, &rec.Content); err != nil {
			return nil, err
		}
		rec.Kind = ArchiveKind(kind)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Drop discards all of a scope's cold records and returns how many were
// removed. The archive is explicitly droppable once its summary is confirmed
// sufficient (§5.7.9).
func (s *TranscriptArchiveStore) Drop(ctx context.Context, scope string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM transcript_archive WHERE scope = ?`, scope)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
