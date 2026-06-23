package mesh

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"plexus/protocol"
)

// startJS spins up an in-process JetStream NATS server on a random port with a
// persistent file store under t.TempDir() (so the store outlives an agent that
// dies and reconnects). It mirrors server.StartEmbeddedNATS but is inlined here
// because the mesh package cannot import server (server imports mesh).
func startJS(t *testing.T) *natsserver.Server {
	t.Helper()
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random free port
		NoLog:     true,
		NoSigs:    true,
		JetStream: true,
		StoreDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new embedded nats: %v", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded nats did not start")
	}
	t.Cleanup(func() { ns.Shutdown(); ns.WaitForShutdown() })
	return ns
}

// publishToInbox publishes a payload to an agent's inbox over JetStream with the
// given Nats-Msg-Id (for dedup), as a peer (server or node) would.
func publishToInbox(t *testing.T, url, agentID, msgID string, payload []byte) {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := EnsureAgentWorkStream(ctx, js, "agent."); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}
	msg := protocol.Message{ID: msgID, Target: agentID, Type: protocol.TypeP2P, Payload: payload}
	data, _ := json.Marshal(msg)
	if _, err := js.Publish(ctx, "agent."+agentID+".inbox", data, jetstream.WithMsgID(msgID)); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

// TestDurableInboxReplaysAfterReconnect is the E1.2 acceptance: a message sent to
// an agent that is down is retained on the durable inbox stream and replayed when
// the agent reconnects under the same durable consumer. The killed-then-restarted
// node is simulated by cancelling one Run() and starting another with the same id.
func TestDurableInboxReplaysAfterReconnect(t *testing.T) {
	ns := startJS(t)
	url := ns.ClientURL()

	got := make(chan protocol.Message, 8)
	mkNode := func() *Node {
		return NewNode("rip",
			WithNatsURL(url),
			WithOnMessage(func(m protocol.Message) { got <- m }),
		)
	}

	// First incarnation: connect (creating the durable consumer), then die.
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { _ = mkNode().Run(ctx1) }()
	time.Sleep(300 * time.Millisecond) // let the durable consumer register
	cancel1()
	time.Sleep(200 * time.Millisecond) // let Consume stop

	// Agent is now down. A peer sends it work — retained on the stream.
	publishToInbox(t, url, "rip", "m1", []byte("while-you-were-out"))

	// Second incarnation reconnects under the same durable and must replay it.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = mkNode().Run(ctx2) }()

	select {
	case m := <-got:
		if string(m.Payload) != "while-you-were-out" {
			t.Fatalf("replayed payload = %q", m.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("message not replayed after reconnect — inbox not durable")
	}
}

// TestDurableInboxDedup verifies Nats-Msg-Id idempotency: two publishes with the
// same id within the dedup window deliver the work exactly once.
func TestDurableInboxDedup(t *testing.T) {
	ns := startJS(t)
	url := ns.ClientURL()

	got := make(chan protocol.Message, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = NewNode("dup",
			WithNatsURL(url),
			WithOnMessage(func(m protocol.Message) { got <- m }),
		).Run(ctx)
	}()
	time.Sleep(300 * time.Millisecond)

	publishToInbox(t, url, "dup", "same-id", []byte("once"))
	publishToInbox(t, url, "dup", "same-id", []byte("once")) // duplicate

	// Exactly one delivery; a second within a short window means dedup failed.
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("first delivery never arrived")
	}
	select {
	case m := <-got:
		t.Fatalf("duplicate delivered (dedup failed): %q", m.Payload)
	case <-time.After(1 * time.Second):
		// good — no second delivery
	}
}
