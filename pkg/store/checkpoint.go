package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Status is the lifecycle state of a Checkpoint (= one step), per §5.7.9.
type Status int

const (
	Pending   Status = iota // 未开始
	Active                  // 进行中 —— resume 重进此步
	Done                    // 已完成，Result 持有蒸馏产出
	Blocked                 // 受阻
	Suspended               // 挂起，等 WaitFor 的 answer 到达（§5.7.5 yield）
)

func (s Status) String() string {
	switch s {
	case Pending:
		return "Pending"
	case Active:
		return "Active"
	case Done:
		return "Done"
	case Blocked:
		return "Blocked"
	case Suspended:
		return "Suspended"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// Checkpoint is one step of a task and the unit of resume — "checkpoints as
// steps" (§5.7.9). A task's plan/progress IS the ordered chain of its
// checkpoints; reading each Goal in Seq order yields the human-facing step
// list. This struct matches the canonical definition in the design doc field
// for field — no serialized mid-stream LLM state, no raw transcript (that lives
// in TranscriptArchive, droppable).
type Checkpoint struct {
	TaskID  string // 属于哪个 task
	Seq     int64  // 链中次序（这一步的位置）
	Goal    string // 这一步的目标（给人看的字段）
	Status  Status // Pending | Active | Done | Blocked | Suspended
	Result  string // 这一步的蒸馏产出（喂下一步 / resume 上下文；可空）
	WaitFor string // 挂起时在等的 CorrelationID（空 = 未挂起，§5.7.5）
}

// Errors returned by CheckpointStore. ErrConflict means the row exists but is
// not in a state from which the requested transition is allowed.
var (
	ErrNotFound = errors.New("store: checkpoint not found")
	ErrConflict = errors.New("store: checkpoint state transition not allowed")
)

const checkpointSchema = `
CREATE TABLE IF NOT EXISTS checkpoints (
    task_id  TEXT    NOT NULL,
    seq      INTEGER NOT NULL,
    goal     TEXT    NOT NULL,
    status   INTEGER NOT NULL,
    result   TEXT    NOT NULL DEFAULT '',
    wait_for TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (task_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_checkpoints_wait_for ON checkpoints (wait_for) WHERE wait_for <> '';
`

// CheckpointStore persists the checkpoint chain (§5.7.9). It is table-scoped —
// it owns the `checkpoints` table, not the database — so it may share a *sql.DB
// with other brain-private stores (WorkingMemory does). The table is keyed
// (task_id, seq); Status/Result/WaitFor update in place.
type CheckpointStore struct {
	db *sql.DB
}

// NewCheckpointStore migrates the schema on db and returns a store over it.
func NewCheckpointStore(ctx context.Context, db *sql.DB) (*CheckpointStore, error) {
	if _, err := db.ExecContext(ctx, checkpointSchema); err != nil {
		return nil, fmt.Errorf("store: migrate checkpoints: %w", err)
	}
	return &CheckpointStore{db: db}, nil
}

// Append adds a new Pending step to the end of a task's checkpoint chain and
// returns it. Seq is assigned atomically as max(seq)+1 for the task (first step
// is Seq 0). The insert and seq computation run in one transaction so concurrent
// Appends to the same task cannot collide on the primary key.
func (s *CheckpointStore) Append(ctx context.Context, taskID, goal string) (Checkpoint, error) {
	if taskID == "" {
		return Checkpoint{}, errors.New("store: empty task id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Checkpoint{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq)+1, 0) FROM checkpoints WHERE task_id = ?`, taskID,
	).Scan(&seq); err != nil {
		return Checkpoint{}, fmt.Errorf("store: next seq: %w", err)
	}
	cp := Checkpoint{TaskID: taskID, Seq: seq, Goal: goal, Status: Pending}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO checkpoints (task_id, seq, goal, status, result, wait_for) VALUES (?, ?, ?, ?, ?, ?)`,
		cp.TaskID, cp.Seq, cp.Goal, int(cp.Status), cp.Result, cp.WaitFor,
	); err != nil {
		return Checkpoint{}, fmt.Errorf("store: insert checkpoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Checkpoint{}, err
	}
	return cp, nil
}

// Activate moves a step into Active and clears any WaitFor. It is both "start
// the next Pending step" and "wake a Suspended step whose answer arrived"
// (§5.7.5) — the only two states from which a step may become Active.
func (s *CheckpointStore) Activate(ctx context.Context, taskID string, seq int64) error {
	return s.guard(ctx, taskID, seq,
		`UPDATE checkpoints SET status = ?, wait_for = '' WHERE task_id = ? AND seq = ? AND status IN (?, ?)`,
		int(Active), taskID, seq, int(Pending), int(Suspended))
}

// Complete moves an Active step to Done, recording its distilled Result (fed to
// the next step / resume context).
func (s *CheckpointStore) Complete(ctx context.Context, taskID string, seq int64, result string) error {
	return s.guard(ctx, taskID, seq,
		`UPDATE checkpoints SET status = ?, result = ? WHERE task_id = ? AND seq = ? AND status = ?`,
		int(Done), result, taskID, seq, int(Active))
}

// Block marks an Active step as Blocked.
func (s *CheckpointStore) Block(ctx context.Context, taskID string, seq int64) error {
	return s.guard(ctx, taskID, seq,
		`UPDATE checkpoints SET status = ? WHERE task_id = ? AND seq = ? AND status = ?`,
		int(Blocked), taskID, seq, int(Active))
}

// Suspend yields an Active step: it records the CorrelationID being waited on
// and flips status to Suspended (§5.7.5). The goroutine may then die; the step
// is woken via Activate when the answer arrives. waitFor must be non-empty — a
// suspension with nothing to wait on is meaningless.
func (s *CheckpointStore) Suspend(ctx context.Context, taskID string, seq int64, waitFor string) error {
	if waitFor == "" {
		return errors.New("store: suspend requires a non-empty WaitFor correlation id")
	}
	return s.guard(ctx, taskID, seq,
		`UPDATE checkpoints SET status = ?, wait_for = ? WHERE task_id = ? AND seq = ? AND status = ?`,
		int(Suspended), waitFor, taskID, seq, int(Active))
}

// guard runs a status-guarded UPDATE and translates a zero-row result into the
// precise error: ErrNotFound if the row is absent, ErrConflict if it exists but
// is in a disallowed state.
func (s *CheckpointStore) guard(ctx context.Context, taskID string, seq int64, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return s.missReason(ctx, taskID, seq)
	}
	return nil
}

func (s *CheckpointStore) missReason(ctx context.Context, taskID string, seq int64) error {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM checkpoints WHERE task_id = ? AND seq = ?`, taskID, seq).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	default:
		return ErrConflict
	}
}

// Get returns a single checkpoint, or ErrNotFound.
func (s *CheckpointStore) Get(ctx context.Context, taskID string, seq int64) (Checkpoint, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT task_id, seq, goal, status, result, wait_for FROM checkpoints WHERE task_id = ? AND seq = ?`,
		taskID, seq)
	cp, err := scanCheckpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{}, ErrNotFound
	}
	return cp, err
}

// Steps returns a task's full checkpoint chain in Seq order — the step list.
// Read each Goal for the human-facing plan; read each Status for progress.
func (s *CheckpointStore) Steps(ctx context.Context, taskID string) ([]Checkpoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, seq, goal, status, result, wait_for FROM checkpoints WHERE task_id = ? ORDER BY seq`,
		taskID)
	if err != nil {
		return nil, err
	}
	return scanCheckpoints(rows)
}

// Active returns a task's current Active step (lowest Seq if more than one) and
// true, or false if none is active — the entry point for resume: re-enter the
// Active step with context = role card + task goal + prior steps' Goal+Result.
func (s *CheckpointStore) Active(ctx context.Context, taskID string) (Checkpoint, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT task_id, seq, goal, status, result, wait_for FROM checkpoints WHERE task_id = ? AND status = ? ORDER BY seq LIMIT 1`,
		taskID, int(Active))
	cp, err := scanCheckpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	return cp, true, nil
}

// Waiting returns every Suspended checkpoint (across all tasks) waiting on the
// given CorrelationID. When an answer arrives the runtime looks up its waiters
// here, then Activates each to wake them (§5.7.5).
func (s *CheckpointStore) Waiting(ctx context.Context, correlationID string) ([]Checkpoint, error) {
	if correlationID == "" {
		return nil, errors.New("store: empty correlation id")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, seq, goal, status, result, wait_for FROM checkpoints WHERE wait_for = ? AND status = ? ORDER BY task_id, seq`,
		correlationID, int(Suspended))
	if err != nil {
		return nil, err
	}
	return scanCheckpoints(rows)
}

type scannable interface {
	Scan(dest ...any) error
}

func scanCheckpoint(row scannable) (Checkpoint, error) {
	var cp Checkpoint
	var status int
	if err := row.Scan(&cp.TaskID, &cp.Seq, &cp.Goal, &status, &cp.Result, &cp.WaitFor); err != nil {
		return Checkpoint{}, err
	}
	cp.Status = Status(status)
	return cp, nil
}

func scanCheckpoints(rows *sql.Rows) ([]Checkpoint, error) {
	defer func() { _ = rows.Close() }()
	var out []Checkpoint
	for rows.Next() {
		cp, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}
