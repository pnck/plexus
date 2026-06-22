// Package store implements the agent's local persistence layer: the four
// state/memory types of §5.7.9 (WorkingMemory, TranscriptArchive,
// LongTermMemory, Checkpoint), each backed by its OWN SQLite database — they do
// not share a connection. This file provides the shared SQLite layer; the
// per-type stores live alongside it (checkpoint.go first).
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver: no CGo, keeps plexus a single static binary inside bwrap
)

// Open opens (creating if absent) the SQLite database at path with the pragmas
// the agent's local stores depend on:
//
//   - journal_mode(WAL)      — crash-safe single-writer durability (process can
//     die mid-yield and the database stays consistent, §5.7.5).
//   - busy_timeout(5000)     — concurrent goroutines retry for 5s instead of
//     erroring out with SQLITE_BUSY.
//   - foreign_keys(ON)       — enforce referential integrity where declared.
//   - synchronous(NORMAL)    — the WAL-recommended durability/throughput point.
//
// Pass ":memory:" for an ephemeral database (tests). Because each of the four
// §5.7.9 stores owns its own database, callers Open one handle per store.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	// A local agent store is single-writer with low concurrency. Capping at one
	// connection serializes statements at the database/sql layer — sidestepping
	// SQLITE_BUSY between goroutines — and is also what keeps an in-memory
	// (":memory:") database alive for the lifetime of the handle rather than
	// being torn down with each idle connection.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", path, err)
	}
	return db, nil
}
