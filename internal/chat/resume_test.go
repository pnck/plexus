package chat

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"plexus/pkg/llm"
	"plexus/pkg/mesh"
	"plexus/protocol"
	"plexus/server"
)

// TestCrossProcessYieldResume is the E1.4 acceptance (§5.7.5): an agent suspends a
// step awaiting approval, the agent process dies, the answer is delivered to its
// durable inbox while it is down, and a FRESH agent over the same persistent stores
// reconnects, replays the retained answer, and resumes — rebuilding context from
// the persisted step chain (never replaying the dead transcript).
//
// The embedded NATS broker (with a persistent JetStream file store) stays up across
// the agent restart — modelling an external NATS cluster in production — while only
// the agent (its mesh node + brain) is killed and re-created. The brain-private
// SQLite checkpoint store is a file shared by both incarnations.
func TestCrossProcessYieldResume(t *testing.T) {
	port := freePort(t)
	jsStore := t.TempDir()                          // persistent JetStream store (outlives the agent)
	dbPath := filepath.Join(t.TempDir(), "brain.db") // persistent brain checkpoint store
	url := "nats://127.0.0.1:" + strconv.Itoa(port)

	ns, err := server.StartEmbeddedNATS(port, jsStore)
	if err != nil {
		t.Fatalf("embedded nats: %v", err)
	}
	t.Cleanup(func() { ns.Shutdown(); ns.WaitForShutdown() })

	// Control plane captures the agent's frames (the approval ask, then the reply).
	frames := make(chan inFrame, 64)
	srvCtx, srvCancel := context.WithCancel(context.Background())
	t.Cleanup(srvCancel)
	srv := server.New(server.WithNatsURL(url), server.WithOnReport(func(m protocol.Message) {
		if f, ok := decodeFrame(m.Payload); ok {
			frames <- inFrame{f: f, corr: m.CorrelationID}
		}
	}))
	go func() { _ = srv.Run(srvCtx) }()
	time.Sleep(300 * time.Millisecond)

	const agentID = "chat-agent"
	awaitFrame := func(kind string) inFrame {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case in := <-frames:
				if in.f.Kind == kind {
					return in
				}
			case <-timeout:
				t.Fatalf("timeout waiting for %q frame", kind)
			}
		}
	}
	startHost := func(gw llm.Provider) (*Host, context.CancelFunc) {
		t.Helper()
		hctx, hcancel := context.WithCancel(context.Background())
		h, err := NewHost(hctx, agentID, Config{
			Gateway: gw, DBPath: dbPath, IncludeRunCommand: true,
		}, mesh.WithNatsURL(url))
		if err != nil {
			t.Fatalf("NewHost: %v", err)
		}
		go func() { _ = h.Run(hctx) }()
		deadline := time.Now().Add(5 * time.Second)
		for !registered(srv, agentID) {
			if time.Now().After(deadline) {
				t.Fatal("agent did not register")
			}
			time.Sleep(20 * time.Millisecond)
		}
		return h, hcancel
	}
	send := func(corr string, f Frame, task string) {
		if err := srv.SendP2P(context.Background(), agentID, protocol.Message{
			CorrelationID: corr, TaskID: task, Payload: encodeFrame(f),
		}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}

	// ── Incarnation 1: drive a turn that yields on a gated run_command, then die.
	gw1 := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "c1", Name: "run_command", Arguments: `{"command":"deploy"}`}}},
	}}
	h1, cancel1 := startHost(gw1)
	send("turn-1", Frame{Kind: kindSay, Text: "deploy the service"}, DefaultTaskID)

	ask := awaitFrame(kindApproval)
	yieldCorr := ask.corr // the correlation id the suspended step waits on

	// Kill the agent: cancel its context and close it (flushing the checkpoint DB).
	cancel1()
	_ = h1.Close()
	time.Sleep(300 * time.Millisecond)

	// The answer is delivered to the agent's DURABLE inbox while it is down — it is
	// retained on the JetStream stream and replayed when the agent reconnects.
	send(yieldCorr, Frame{Kind: kindAnswer, Text: approveWord}, "")

	// ── Incarnation 2: a fresh agent over the same persistent stores. It has no
	// in-memory state; the retained answer replays and it resumes from the step chain.
	gw2 := &fakeGateway{turns: []scriptedTurn{
		{text: "resumed and completed the deploy"},
	}}
	_, cancel2 := startHost(gw2)
	t.Cleanup(cancel2)

	reply := awaitFrame(kindReply)
	if reply.f.Text != "resumed and completed the deploy" {
		t.Fatalf("resumed reply = %q (fresh agent did not resume from the durable answer)", reply.f.Text)
	}
}
