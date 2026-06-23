package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// NoteSource records which of the two write paths (§5.7.9) produced a working
// memory note: an explicit agent write (mem_write) or a system compaction that
// distilled old frames into a reminder.
type NoteSource int

const (
	Manual  NoteSource = iota // agent wrote it explicitly (mem_write)
	Compact                   // system distilled it from compacted frames
)

func (s NoteSource) String() string {
	switch s {
	case Manual:
		return "manual"
	case Compact:
		return "compact"
	default:
		return fmt.Sprintf("NoteSource(%d)", int(s))
	}
}

// Note is one working memory entry: a distilled reminder the agent recalls on
// demand (§5.7.9). It does NOT enter the context window automatically — only a
// mem_read pulls it in. Notes are keyed within a scope (the task) so the agent
// can address them by topic.
type Note struct {
	Scope   string // owning scope (task id) — working memory is task-scoped
	Key     string // topic label the agent addresses the note by
	Content string // the distilled reminder
	Source  NoteSource
}

const workingMemorySchema = `
CREATE TABLE IF NOT EXISTS working_memory (
    scope   TEXT    NOT NULL,
    key     TEXT    NOT NULL,
    content TEXT    NOT NULL,
    source  INTEGER NOT NULL,
    PRIMARY KEY (scope, key)
);
`

// WorkingMemoryStore persists WorkingMemory (§5.7.9). It wraps its own *sql.DB —
// not shared with the other state/memory stores. Notes are recall-on-demand:
// nothing here reaches the LLM until the agent explicitly reads it.
type WorkingMemoryStore struct {
	db *sql.DB
}

// NewWorkingMemoryStore migrates the schema on db and returns a store over it.
func NewWorkingMemoryStore(ctx context.Context, db *sql.DB) (*WorkingMemoryStore, error) {
	if _, err := db.ExecContext(ctx, workingMemorySchema); err != nil {
		return nil, fmt.Errorf("store: migrate working_memory: %w", err)
	}
	return &WorkingMemoryStore{db: db}, nil
}

// Put upserts a note: a later write to the same (scope, key) overwrites the
// earlier one. Both write paths (Manual / Compact) land here.
func (s *WorkingMemoryStore) Put(ctx context.Context, scope, key, content string, source NoteSource) error {
	if scope == "" || key == "" {
		return errors.New("store: working memory requires non-empty scope and key")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO working_memory (scope, key, content, source) VALUES (?, ?, ?, ?)
         ON CONFLICT(scope, key) DO UPDATE SET content = excluded.content, source = excluded.source`,
		scope, key, content, int(source))
	if err != nil {
		return fmt.Errorf("store: put working memory: %w", err)
	}
	return nil
}

// Get returns a single note, or ErrNotFound.
func (s *WorkingMemoryStore) Get(ctx context.Context, scope, key string) (Note, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT scope, key, content, source FROM working_memory WHERE scope = ? AND key = ?`,
		scope, key)
	n, err := scanNote(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, ErrNotFound
	}
	return n, err
}

// List returns every note in a scope, ordered by key for stable output.
func (s *WorkingMemoryStore) List(ctx context.Context, scope string) ([]Note, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scope, key, content, source FROM working_memory WHERE scope = ? ORDER BY key`,
		scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Delete removes a note. Deleting an absent note is a no-op (no error) — the
// caller's intent (the note is gone) is satisfied either way.
func (s *WorkingMemoryStore) Delete(ctx context.Context, scope, key string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM working_memory WHERE scope = ? AND key = ?`, scope, key)
	return err
}

func scanNote(row scannable) (Note, error) {
	var n Note
	var source int
	if err := row.Scan(&n.Scope, &n.Key, &n.Content, &source); err != nil {
		return Note{}, err
	}
	n.Source = NoteSource(source)
	return n, nil
}
