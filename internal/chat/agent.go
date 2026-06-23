package chat

import (
	"context"
	"fmt"

	"database/sql"

	"plexus/pkg/brain"
	"plexus/pkg/effector"
	"plexus/pkg/llm"
	"plexus/pkg/store"
	"plexus/protocol"
)

// Config assembles a chat Agent. Gateway is required; the rest default.
type Config struct {
	// Gateway is the LLM provider the brain drives (required).
	Gateway llm.Provider
	// DBPath is the brain-private SQLite database holding the Checkpoint and
	// WorkingMemory tables (§5.7.9 revised: one connection, two tables). Use
	// ":memory:" for tests. TranscriptArchive is intentionally NOT opened here —
	// there is no compaction/archiving need yet.
	DBPath string
	// RoleCard overrides the chat default; zero value uses DefaultRoleCard().
	RoleCard brain.RoleCard
	// Approver gates approval-required effectors; nil denies all (DenyApprover).
	// The bus host supplies the interactive /approve /deny approver.
	Approver brain.Approver
	// IncludeRunCommand registers the run_command effector (ExecArbitrary,
	// approval-gated). Off by default — the approval-free primitives cover
	// ordinary work; arbitrary shell is opt-in.
	IncludeRunCommand bool
	// OnDelta / OnUsage / OnTool are the brain's live sinks (host wires them to the
	// bus). Optional.
	OnDelta func(string)
	OnUsage func(llm.Usage)
	OnTool  func(name, args, result string)
}

// Agent is a fully assembled chat agent: an LLM gateway, the built-in effector
// registry (with the brain-private memory + checkpoint stores), and a brain
// seeded with the chat role card and a task-channel that rejects task_* (chat's
// standing task is an open-ended pseudo-task). It owns its SQLite connection.
type Agent struct {
	Brain         *brain.Brain
	Registry      *effector.Registry        // exposed for the /tools control command
	Checkpoints   *store.CheckpointStore    // exposed for the /steps control command
	WorkingMemory *store.WorkingMemoryStore // exposed for the /memory control command
	db            *sql.DB
}

// New assembles an Agent. It opens one brain-private SQLite connection shared by
// the Checkpoint and WorkingMemory stores, registers the built-in effectors over
// them, and constructs the brain with the chat role card, a deny-by-default
// approver, and the reject emitter. No NATS — the bus host is a later node.
func New(ctx context.Context, cfg Config) (*Agent, error) {
	if cfg.Gateway == nil {
		return nil, fmt.Errorf("chat: nil gateway")
	}
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("chat: empty DBPath")
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("chat: open brain-private db: %w", err)
	}
	// Checkpoint + WorkingMemory share this one connection (table-scoped stores).
	checkpoints, err := store.NewCheckpointStore(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("chat: checkpoint store: %w", err)
	}
	workingMemory, err := store.NewWorkingMemoryStore(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("chat: working memory store: %w", err)
	}

	reg := effector.NewRegistry(nil)
	effector.RegisterBuiltins(reg, effector.BuiltinOptions{
		WorkingMemory:     workingMemory,
		Checkpoints:       checkpoints,
		IncludeRunCommand: cfg.IncludeRunCommand,
	})

	roleCard := cfg.RoleCard
	if roleCard.SystemPrompt == "" {
		roleCard = DefaultRoleCard()
	}
	var approver brain.Approver = brain.DenyApprover{}
	if cfg.Approver != nil {
		approver = cfg.Approver
	}

	b := brain.New(brain.Options{
		Gateway:  cfg.Gateway,
		Registry: reg,
		RoleCard: roleCard,
		Approver: approver,
		Emitter:  rejectEmitter{}, // chat rejects task_* (open-ended pseudo-task)
		OnDelta:  cfg.OnDelta,
		OnUsage:  cfg.OnUsage,
		OnTool:   cfg.OnTool,
	})

	return &Agent{
		Brain:         b,
		Registry:      reg,
		Checkpoints:   checkpoints,
		WorkingMemory: workingMemory,
		db:            db,
	}, nil
}

// Handle runs one user turn through the brain under the standing chat task, and
// returns the agent's reply. This is the in-process entry point (the bus host
// will instead feed the brain's Inbound).
func (a *Agent) Handle(ctx context.Context, text string) (string, error) {
	return a.Brain.Handle(ctx, protocol.Message{
		Type:    protocol.TypeP2P, // direct human message -> L2 user instruction
		Sender:  "user",
		TaskID:  DefaultTaskID,
		Payload: []byte(text),
	})
}

// Close releases the agent's SQLite connection.
func (a *Agent) Close() error {
	return a.db.Close()
}
