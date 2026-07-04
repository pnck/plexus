// Package agent is the tag-free, readline-free assembly of a plexus agent: an LLM
// gateway, the built-in effector registry over the brain-private memory + checkpoint
// stores, and a brain seeded with a role card. It is shared by the interactive chat
// host (which layers a REPL + live-reconfigurable gateway on top) and the headless
// `run` daemon (which drives the brain off bus messages) — so neither drags in the
// other's dependencies.
package agent

import (
	"context"
	"database/sql"
	"fmt"

	"plexus/pkg/brain"
	"plexus/pkg/effector"
	"plexus/pkg/llm"
	"plexus/pkg/store"
	"plexus/protocol"
)

// Config assembles an Agent. Gateway and DBPath are required; the rest default.
type Config struct {
	// Gateway is the LLM provider the brain drives (required).
	Gateway llm.Provider
	// DBPath is the brain-private SQLite database holding the Checkpoint and
	// WorkingMemory tables. Use ":memory:" for tests.
	DBPath string
	// RoleCard seeds the brain (identity + permitted effects). The zero value is a
	// minimal card; the caller supplies the real one (chat's default, or the one
	// loaded from the sandbox role-card path).
	RoleCard brain.RoleCard
	// EnvState is the rendered sandbox environment-state L1 frame (from
	// sandbox.Environment.Describe()) — the agent's concrete fs/net/limit constraints.
	// Empty omits the frame (e.g. an un-sandboxed dev run).
	EnvState string
	// Emitter receives the brain's task_* domain events. Nil defaults to RejectEmitter
	// (an agent with no task DAG). The bus host supplies a real emitter later.
	Emitter brain.Emitter
	// Approver gates approval-required effectors; nil denies all (DenyApprover).
	Approver brain.Approver
	// IncludeRunCommand registers the run_command effector (arbitrary shell,
	// approval-gated). Off by default.
	IncludeRunCommand bool
	// YieldForApproval switches the approval gate from the synchronous Approver to
	// durable yield/resume: a gated effector suspends a step and the brain returns a
	// *brain.YieldError, woken by Brain.Resume.
	YieldForApproval bool
	// OnDelta / OnThinking / OnUsage / OnToolStart / OnTool / OnDelegTrace are the
	// brain's live sinks (a host wires them to the bus / REPL). Optional.
	OnDelta      func(string)
	OnThinking   func(string)
	OnUsage      func(llm.Usage)
	OnToolStart  func(name, args string)
	OnTool       func(name, args, result string)
	OnDelegTrace func(string)
}

// Agent is a fully assembled agent: the brain, the built-in effector registry (over
// the brain-private memory + checkpoint stores), and the SQLite connection it owns.
// It carries no transport — a chat host or the run daemon connects it to a bus.
type Agent struct {
	Brain         *brain.Brain
	Registry      *effector.Registry
	Checkpoints   *store.CheckpointStore
	WorkingMemory *store.WorkingMemoryStore
	db            *sql.DB
}

// New assembles an Agent: one brain-private SQLite connection shared by the
// Checkpoint + WorkingMemory stores, the built-in effectors over them, and a brain
// seeded with the role card, a deny-by-default approver, and the configured emitter.
// No transport — a host connects it to a bus.
func New(ctx context.Context, cfg Config) (*Agent, error) {
	if cfg.Gateway == nil {
		return nil, fmt.Errorf("agent: nil gateway")
	}
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("agent: empty DBPath")
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("agent: open brain-private db: %w", err)
	}
	checkpoints, err := store.NewCheckpointStore(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agent: checkpoint store: %w", err)
	}
	workingMemory, err := store.NewWorkingMemoryStore(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agent: working memory store: %w", err)
	}

	// Gating policy from the role card's effect grant (E3.3): a non-empty permitted
	// set -> PermittedPolicy; empty -> DefaultPolicy in NewRegistry.
	permitted, err := cfg.RoleCard.PermittedSet()
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agent: role card permitted: %w", err)
	}
	var policy effector.Policy
	if !permitted.IsEmpty() {
		policy = effector.PermittedPolicy{Permitted: permitted}
	}
	reg := effector.NewRegistry(policy)
	effector.RegisterBuiltins(reg, effector.BuiltinOptions{
		WorkingMemory:     workingMemory,
		Checkpoints:       checkpoints,
		IncludeRunCommand: cfg.IncludeRunCommand,
	})

	var approver brain.Approver = brain.DenyApprover{}
	if cfg.Approver != nil {
		approver = cfg.Approver
	}
	var emitter brain.Emitter = RejectEmitter{}
	if cfg.Emitter != nil {
		emitter = cfg.Emitter
	}

	b := brain.New(brain.Options{
		Gateway:          cfg.Gateway,
		Registry:         reg,
		RoleCard:         cfg.RoleCard,
		EnvState:         cfg.EnvState,
		Approver:         approver,
		Emitter:          emitter,
		Checkpoints:      checkpoints,
		YieldForApproval: cfg.YieldForApproval,
		OnDelta:          cfg.OnDelta,
		OnThinking:       cfg.OnThinking,
		OnUsage:          cfg.OnUsage,
		OnToolStart:      cfg.OnToolStart,
		OnTool:           cfg.OnTool,
		OnDelegTrace:     cfg.OnDelegTrace,
	})

	return &Agent{
		Brain:         b,
		Registry:      reg,
		Checkpoints:   checkpoints,
		WorkingMemory: workingMemory,
		db:            db,
	}, nil
}

// Handle runs one user turn through the brain scoped to taskID, returning the reply.
// A convenience for in-process drivers; a bus host instead drives Brain.Handle off
// pushed bus messages.
func (a *Agent) Handle(ctx context.Context, taskID, text string) (string, error) {
	return a.Brain.Handle(ctx, protocol.Message{
		Type:    protocol.TypeP2P,
		Sender:  "user",
		TaskID:  taskID,
		Payload: []byte(text),
	})
}

// Close releases the agent's SQLite connection.
func (a *Agent) Close() error { return a.db.Close() }
