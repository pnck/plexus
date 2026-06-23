package chat

import (
	"context"
	"fmt"
	"io"
	"time"

	"plexus/pkg/brain"
	"plexus/pkg/llm"
	"plexus/pkg/mesh"
	"plexus/server"
)

// RunConfig configures a `plexus chat` session.
type RunConfig struct {
	Gateway           llm.Provider   // required: the assembled brain's LLM gateway
	RoleCard          brain.RoleCard // optional override of the chat default role card
	AgentID           string         // agent id on the mesh (default "chat-agent")
	NatsPort          int            // embedded NATS port (default 4222)
	IncludeRunCommand bool           // register run_command (ExecArbitrary, approval-gated)
}

// Run starts a self-contained chat session: an embedded NATS server, the
// assembled agent hosted on a mesh node, a control-plane server, and the REPL
// client — all in one process (E2.6.5). The user talks to the agent only through
// the control plane (over the bus); the brain is never touched directly. Run
// returns when the user exits or in/ctx ends.
func Run(parent context.Context, cfg RunConfig, in io.Reader, out io.Writer) error {
	if cfg.Gateway == nil {
		return fmt.Errorf("chat: nil gateway")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel() // exiting the REPL tears down the host + control plane goroutines

	port := cfg.NatsPort
	if port == 0 {
		port = 4222
	}
	agentID := cfg.AgentID
	if agentID == "" {
		agentID = "chat-agent"
	}

	// 1. Embedded NATS — the bus the whole session rides on.
	ns, err := server.StartEmbeddedNATS(port)
	if err != nil {
		return fmt.Errorf("chat: start embedded NATS: %w", err)
	}
	defer func() { ns.Shutdown(); ns.WaitForShutdown() }()
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)

	// 2. Control plane + REPL client — started FIRST and given a moment to
	// subscribe. On core NATS the agent's one-shot registration is fire-and-
	// forget: if it published before the control plane subscribed to sys.register
	// it would be lost. So the control plane must be listening before the host
	// starts. (Durable registration that removes this startup race is E1.2.)
	c := &client{agentID: agentID, out: out}
	srv := server.New(server.WithNatsURL(url), server.WithOnReport(c.onReport))
	c.srv = srv
	go func() { _ = srv.Run(ctx) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(300 * time.Millisecond): // let the control plane subscribe
	}

	// 3. The agent, assembled and hosted on the bus.
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

	// Wait for the agent to register so the first turn has an inbox subscriber.
	if err := waitRegistered(ctx, srv, agentID, 5*time.Second); err != nil {
		return err
	}
	return c.loop(ctx, in)
}

// waitRegistered blocks until agentID appears in the control plane's registry or
// the timeout elapses.
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
