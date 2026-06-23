package chat

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/chzyer/readline"
	"plexus/pkg/brain"
	"plexus/pkg/llm"
	"plexus/pkg/mesh"
	"plexus/server"
)

// RunConfig configures a `plexus chat` session.
type RunConfig struct {
	Gateway           llm.Provider   // the brain's gateway (a *mutableGateway for the live command)
	RoleCard          brain.RoleCard // optional override of the chat default role card
	AgentID           string         // agent id on the mesh (default "chat-agent")
	NatsPort          int            // embedded NATS port (default 4222)
	IncludeRunCommand bool           // register run_command (ExecArbitrary, approval-gated)
}

// Run starts a self-contained chat session: an embedded NATS server, the
// assembled agent hosted on a mesh node, a control-plane server, and the rich
// REPL client — all in one process. The user talks to the agent only through the
// control plane (over the bus). Run returns when the user exits or ctx ends.
func Run(parent context.Context, cfg RunConfig, in io.Reader, out io.Writer) error {
	if cfg.Gateway == nil {
		return fmt.Errorf("chat: nil gateway")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	port := cfg.NatsPort
	if port == 0 {
		port = 4222
	}
	agentID := cfg.AgentID
	if agentID == "" {
		agentID = "chat-agent"
	}

	ns, err := server.StartEmbeddedNATS(port)
	if err != nil {
		return fmt.Errorf("chat: start embedded NATS: %w", err)
	}
	defer func() { ns.Shutdown(); ns.WaitForShutdown() }()
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)

	// Readline-backed REPL client.
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "\033[36m›\033[0m ",
		AutoComplete:    completer(),
		Stdin:           io.NopCloser(in),
		Stdout:          out,
		InterruptPrompt: "^C",
		EOFPrompt:       "",
		HistoryLimit:    1000,
	})
	if err != nil {
		return fmt.Errorf("chat: readline: %w", err)
	}
	defer func() { _ = rl.Close() }()

	c := &client{agentID: agentID, rl: rl, frames: make(chan inFrame, 64)}

	// Control plane + client first, given a moment to subscribe before the agent
	// registers (core NATS one-shot registration would otherwise be lost; durable
	// registration is E1.2).
	srv := server.New(server.WithNatsURL(url), server.WithOnReport(c.onReport))
	c.srv = srv
	go func() { _ = srv.Run(ctx) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}

	// The agent, assembled and hosted on the bus.
	h, err := NewHost(ctx, agentID, Config{
		Gateway:           cfg.Gateway,
		DBPath:            ":memory:", // single, non-persisted session — no save/resume
		RoleCard:          cfg.RoleCard,
		IncludeRunCommand: cfg.IncludeRunCommand,
	}, mesh.WithNatsURL(url))
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	go func() { _ = h.Run(ctx) }()

	if err := waitRegistered(ctx, srv, agentID, 5*time.Second); err != nil {
		return err
	}
	return c.run(ctx)
}

// waitRegistered blocks until agentID registers or the timeout elapses.
func waitRegistered(ctx context.Context, srv *server.Server, agentID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		for _, id := range srv.GetRegisteredAgents() {
			if id == agentID {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("chat: agent %q did not register within %s", agentID, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
