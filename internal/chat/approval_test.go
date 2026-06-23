package chat

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"plexus/pkg/llm"
	"plexus/pkg/mesh"
	"plexus/protocol"
	"plexus/server"
)

// An approval-required effector (run_command) round-trips to the user over the
// bus: the agent emits an approval-request report, the user answers /approve,
// and the effector then runs. Exercises the whole E2.6.4 path through the host
// demux + busApprover.
func TestApprovalRoundTripOverBus(t *testing.T) {
	port := freePort(t)
	ns, err := server.StartEmbeddedNATS(port)
	if err != nil {
		t.Fatalf("embedded nats: %v", err)
	}
	t.Cleanup(func() { ns.Shutdown(); ns.WaitForShutdown() })
	url := urlFor(port)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Control plane captures reports and lets us answer approvals.
	reports := make(chan protocol.Message, 16)
	srv := server.New(server.WithNatsURL(url), server.WithOnReport(func(m protocol.Message) { reports <- m }))
	go func() { _ = srv.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	// Agent: turn 1 calls run_command (ExecArbitrary → approval), turn 2 converges.
	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "x1", Name: "run_command", Arguments: `{"command":"echo","args":["hi"]}`}}},
		{text: "ran it"},
	}}
	h, err := NewHost(ctx, "chat-agent", Config{
		Gateway:           gw,
		DBPath:            ":memory:",
		IncludeRunCommand: true, // make run_command available (opt-in)
	}, mesh.WithNatsURL(url))
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	go func() { _ = h.Run(ctx) }()

	waitFor(t, func() bool { return registered(srv, "chat-agent") }, "agent register")

	// Send a user turn.
	if err := srv.SendP2P(ctx, "chat-agent", protocol.Message{CorrelationID: "t1", TaskID: DefaultTaskID, Payload: []byte("run echo")}); err != nil {
		t.Fatalf("SendP2P: %v", err)
	}

	// First report must be the approval request.
	req := recv(t, reports, "approval request")
	desc, ok := parseApprovalRequest(string(req.Payload))
	if !ok {
		t.Fatalf("first report not an approval request: %q", req.Payload)
	}
	if !strings.Contains(desc, "run_command") {
		t.Fatalf("approval desc = %q, want it to mention run_command", desc)
	}

	// Answer /approve (carry the request's CorrelationID back).
	if err := srv.SendP2P(ctx, "chat-agent", protocol.Message{CorrelationID: req.CorrelationID, Payload: []byte(approveWord)}); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// The turn now converges; the final reply is paired to the user turn.
	rep := recv(t, reports, "final reply")
	if rep.CorrelationID != "t1" || string(rep.Payload) != "ran it" {
		t.Fatalf("final reply = {corr:%q payload:%q}, want {t1, ran it}", rep.CorrelationID, rep.Payload)
	}
}

// A /deny answer refuses the effector; the brain is told and converges without
// running it.
func TestApprovalDenyOverBus(t *testing.T) {
	port := freePort(t)
	ns, err := server.StartEmbeddedNATS(port)
	if err != nil {
		t.Fatalf("embedded nats: %v", err)
	}
	t.Cleanup(func() { ns.Shutdown(); ns.WaitForShutdown() })
	url := urlFor(port)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reports := make(chan protocol.Message, 16)
	srv := server.New(server.WithNatsURL(url), server.WithOnReport(func(m protocol.Message) { reports <- m }))
	go func() { _ = srv.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "x1", Name: "run_command", Arguments: `{"command":"rm","args":["-rf","/"]}`}}},
		{text: "ok, not running that"},
	}}
	h, err := NewHost(ctx, "chat-agent", Config{Gateway: gw, DBPath: ":memory:", IncludeRunCommand: true}, mesh.WithNatsURL(url))
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	go func() { _ = h.Run(ctx) }()
	waitFor(t, func() bool { return registered(srv, "chat-agent") }, "agent register")

	_ = srv.SendP2P(ctx, "chat-agent", protocol.Message{CorrelationID: "t1", TaskID: DefaultTaskID, Payload: []byte("delete everything")})

	req := recv(t, reports, "approval request")
	if _, ok := parseApprovalRequest(string(req.Payload)); !ok {
		t.Fatalf("expected approval request, got %q", req.Payload)
	}
	_ = srv.SendP2P(ctx, "chat-agent", protocol.Message{CorrelationID: req.CorrelationID, Payload: []byte(denyWord)})

	rep := recv(t, reports, "final reply")
	if string(rep.Payload) != "ok, not running that" {
		t.Fatalf("final reply = %q", rep.Payload)
	}
	// The denial must have been fed back to the brain as a DENIED tool result.
	var sawDenied bool
	for _, f := range h.agent.Brain.History() {
		if f.Authority == protocol.AuthTool && strings.Contains(f.Content, "DENIED") {
			sawDenied = true
		}
	}
	if !sawDenied {
		t.Fatal("denial was not fed back into the brain")
	}
}

// --- shared test helpers ---
//nolint:unused // recv/waitFor/registered/urlFor are shared across chat tests

func urlFor(port int) string { return "nats://127.0.0.1:" + strconv.Itoa(port) }

func registered(srv *server.Server, id string) bool {
	for _, a := range srv.GetRegisteredAgents() {
		if a == id {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func recv(t *testing.T, ch <-chan protocol.Message, what string) protocol.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
		return protocol.Message{}
	}
}
