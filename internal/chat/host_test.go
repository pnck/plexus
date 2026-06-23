package chat

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"plexus/pkg/mesh"
	"plexus/protocol"
	"plexus/server"
)

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// freePort returns a currently-unused localhost TCP port so the embedded NATS
// server does not clash with other packages' tests (the mesh smoke uses 4222).
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// A user turn delivered over NATS reaches the hosted brain and its reply comes
// back on the report subject, paired by CorrelationID. Exercises the whole
// E2.6.2 bridge: control plane -> inbox -> channelInbound -> brain.Step ->
// sys.report -> control plane.
func TestHostRoundTripOverBus(t *testing.T) {
	port := freePort(t)
	ns, err := server.StartEmbeddedNATS(port)
	if err != nil {
		t.Fatalf("embedded nats: %v", err)
	}
	t.Cleanup(func() { ns.Shutdown(); ns.WaitForShutdown() })
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Control plane (the "client"): captures the agent's reports.
	reports := make(chan protocol.Message, 8)
	srv := server.New(
		server.WithNatsURL(url),
		server.WithOnReport(func(m protocol.Message) { reports <- m }),
	)
	go func() { _ = srv.Run(ctx) }()
	time.Sleep(300 * time.Millisecond) // let the control plane subscribe before the node registers

	// Host: a chat agent on the bus, driven by a scripted fake gateway.
	gw := &fakeGateway{turns: []scriptedTurn{{text: "hello back"}}}
	h, err := NewHost(ctx, "chat-agent", Config{Gateway: gw, DBPath: ":memory:"}, mesh.WithNatsURL(url))
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	go func() { _ = h.Run(ctx) }()

	// Registration (sent after the node subscribes its inbox) is our readiness gate.
	deadline := time.Now().Add(3 * time.Second)
	for !contains(srv.GetRegisteredAgents(), "chat-agent") {
		if time.Now().After(deadline) {
			t.Fatal("agent did not register in time")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Send a user turn through the control plane (not directly to the brain).
	if err := srv.SendP2P(ctx, "chat-agent", protocol.Message{
		CorrelationID: "c1",
		TaskID:        DefaultTaskID,
		Payload:       []byte("hello"),
	}); err != nil {
		t.Fatalf("SendP2P: %v", err)
	}

	select {
	case rep := <-reports:
		if rep.Type != protocol.TypeReport {
			t.Fatalf("reply type = %v, want Report", rep.Type)
		}
		if rep.CorrelationID != "c1" {
			t.Fatalf("reply CorrelationID = %q, want c1 (pairing lost)", rep.CorrelationID)
		}
		if string(rep.Payload) != "hello back" {
			t.Fatalf("reply payload = %q, want %q", rep.Payload, "hello back")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reply report received over the bus")
	}
}
