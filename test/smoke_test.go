package test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"plexus/protocol"
	"plexus/server"
)

// freePort returns a currently-unused localhost TCP port so the embedded NATS
// server does not clash with a running `plexus chat` or another test.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func TestMeshCommunicationSmoke(t *testing.T) {
	// 1. Build the test-agent binary in a temporary directory
	tempDir, err := os.MkdirTemp("", "plexus-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	binPath := filepath.Join(tempDir, "plexus-test-smoke_fixture")
	buildCmd := exec.Command("go", "build", "-o", binPath, "plexus/internal/test/smoke_fixture")
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build plexus-test-smoke_fixture binary: %v\n%s", err, string(out))
	}

	// 2. Start embedded NATS server for the test on a free port (so it does not
	// clash with a running `plexus chat` or a parallel test on the default 4222).
	port := freePort(t)
	natsURL := fmt.Sprintf("nats://127.0.0.1:%d", port)
	ns, err := server.StartEmbeddedNATS(port)
	if err != nil {
		t.Fatalf("Failed to start embedded NATS: %v", err)
	}
	t.Cleanup(func() {
		ns.Shutdown()
		ns.WaitForShutdown()
	})

	// 3. Start Control Plane SDK with async report gathering
	reportChan := make(chan protocol.Message, 1000)
	srv := server.New(
		server.WithNatsURL(natsURL),
		server.WithOnReport(func(msg protocol.Message) {
			reportChan <- msg
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		if err := srv.Run(ctx); err != nil {
			t.Logf("Server exited: %v", err)
		}
	}()
	time.Sleep(500 * time.Millisecond) // Let CP connect

	// 4. Start Mock LLM API with atomic counter
	var llmRequests atomic.Int32
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmRequests.Add(1)
		body, _ := io.ReadAll(r.Body)
		time.Sleep(50 * time.Millisecond)
		_, _ = fmt.Fprintf(w, "MOCK_RESPONSE: %s", string(body))
	}))
	t.Cleanup(mockLLM.Close)

	// Helper to spawn an agent
	spawnAgent := func(id, groups, queueGroups string, pingTargets []string) {
		cmd := exec.Command(binPath,
			"--nats-url", natsURL,
			"--id", id,
			"--groups", groups,
			"--queue-groups", queueGroups,
			"--test-llm-url", mockLLM.URL,
			"--test-ping-targets", strings.Join(pingTargets, ","),
		)
		if err := cmd.Start(); err != nil {
			t.Fatalf("Failed to start agent %s: %v", id, err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}

	// Helper to gather metrics for a set duration
	gatherMetrics := func(duration time.Duration) (broadcasts, tasks, llms, p2ps []string) {
		timeout := time.After(duration)
		for {
			select {
			case report := <-reportChan:
				payloadStr := string(report.Payload)
				if strings.Contains(payloadStr, "TypeBroadcast") {
					broadcasts = append(broadcasts, report.Sender)
				} else if strings.Contains(payloadStr, "TypeQueueTask") {
					tasks = append(tasks, report.Sender)
				} else if strings.Contains(payloadStr, "SOURCE:LLM_API") {
					llms = append(llms, payloadStr)
				} else if strings.Contains(payloadStr, "RECEIVED:TypeP2P") {
					p2ps = append(p2ps, report.Sender+"|"+payloadStr)
				}
			case <-timeout:
				return
			}
		}
	}

	// ============================================
	// PHASE 1: STATIC MESH VALIDATION
	// ============================================
	t.Logf("=== PHASE 1: STATIC MESH ===")
	agentIDs := []string{"A", "B", "C"}
	configs := map[string]struct{ groups, queueGroups string }{
		"A": {"frontend", ""},
		"B": {"frontend", "worker_pool:my_workers"},
		"C": {"backend", "worker_pool:my_workers"},
	}

	for _, id := range agentIDs {
		var pings []string
		for _, other := range agentIDs {
			if other != id {
				pings = append(pings, other)
			}
		}
		spawnAgent(id, configs[id].groups, configs[id].queueGroups, pings)
	}

	time.Sleep(2 * time.Second) // Let them boot

	registered := srv.GetRegisteredAgents()
	if len(registered) != 3 {
		t.Errorf("Phase 1: Expected 3 agents registered, got %d", len(registered))
	}
	t.Logf("=> [1/4] Phase 1 Agents registered successfully: %v", registered)

	_ = srv.SendGroupBroadcast(ctx, "frontend", protocol.Message{Payload: []byte("RESTART_FRONTEND")})
	_ = srv.SendGroupTask(ctx, "worker_pool", protocol.Message{Payload: []byte("PROCESS_IMAGE_123")})

	b1, t1, _, p1 := gatherMetrics(3 * time.Second)

	if len(b1) != 2 {
		t.Errorf("Phase 1: Expected 2 agents to receive broadcast, got %d", len(b1))
	}
	if len(t1) != 1 {
		t.Errorf("Phase 1: Expected 1 agent to claim task, got %d", len(t1))
	}
	t.Logf("   ✓ Phase 1 Broadcast and Task assertions passed.")

	expectedP2P1 := map[string]bool{
		"B|RECEIVED:TypeP2P|FROM:A": false,
		"C|RECEIVED:TypeP2P|FROM:A": false,
		"A|RECEIVED:TypeP2P|FROM:B": false,
		"C|RECEIVED:TypeP2P|FROM:B": false,
		"A|RECEIVED:TypeP2P|FROM:C": false,
		"B|RECEIVED:TypeP2P|FROM:C": false,
	}
	for _, r := range p1 {
		if _, exists := expectedP2P1[r]; exists {
			expectedP2P1[r] = true
		}
	}
	for msg, found := range expectedP2P1 {
		if !found {
			t.Errorf("Phase 1: Missing expected P2P message: %s", msg)
		}
	}
	t.Logf("   ✓ Phase 1 P2P Mesh verified.")
	t.Logf("=> [2/4] Phase 1 Broadcasts and Tasks validated successfully.")

	// ============================================
	// PHASE 2: DYNAMIC SUBAGENT INJECTION
	// ============================================
	t.Logf("=== PHASE 2: DYNAMIC SUBAGENT (D) ===")
	// Agent D connects late, targets A and B
	spawnAgent("D", "frontend", "worker_pool:my_workers", []string{"A", "B"})

	time.Sleep(2 * time.Second) // Let D boot and ping

	registered2 := srv.GetRegisteredAgents()
	if len(registered2) != 4 {
		t.Errorf("Phase 2: Expected 4 agents registered, got %d", len(registered2))
	}
	t.Logf("=> [3/4] Phase 2 Dynamic Agent D registered successfully. Total: %d", len(registered2))

	// Send another wave of messages to test dynamic group expansion
	_ = srv.SendGroupBroadcast(ctx, "frontend", protocol.Message{Payload: []byte("RESTART_FRONTEND_V2")})
	_ = srv.SendGroupTask(ctx, "worker_pool", protocol.Message{Payload: []byte("PROCESS_IMAGE_456")})

	b2, t2, _, p2 := gatherMetrics(3 * time.Second)

	// Frontend group should now have A, B, and D (3 receivers)
	if len(b2) != 3 {
		t.Errorf("Phase 2: Expected 3 agents (A,B,D) to receive broadcast, got %d (%v)", len(b2), b2)
	} else {
		t.Logf("   ✓ Phase 2 Broadcast assertion passed. New subagent received broadcast dynamically!")
	}

	// Worker pool has B, C, and D. Only 1 should receive the task.
	if len(t2) != 1 {
		t.Errorf("Phase 2: Expected exactly 1 agent to claim the task, got %d", len(t2))
	} else {
		t.Logf("   ✓ Phase 2 Task assertion passed. Queue pool dynamically balanced.")
	}

	// Verify D's dynamic P2P integration
	expectedP2P2 := map[string]bool{
		"A|RECEIVED:TypeP2P|FROM:D": false,
		"B|RECEIVED:TypeP2P|FROM:D": false,
	}
	for _, r := range p2 {
		if _, exists := expectedP2P2[r]; exists {
			expectedP2P2[r] = true
		}
	}
	for msg, found := range expectedP2P2 {
		if !found {
			t.Errorf("Phase 2: Missing dynamic P2P message from new Subagent D: %s", msg)
		}
	}
	t.Logf("   ✓ Phase 2 P2P Dynamic Injection verified. D successfully pinged A and B.")

	t.Logf("\n--- CONTROL PLANE MESSAGE AGGREGATION DIRECTORY ---")
	t.Logf("Total P2P Reports (Ph1+Ph2): %d", len(p1)+len(p2))
	t.Logf("Total LLM Reports (Ph1+Ph2): %d", int(llmRequests.Load()))
	t.Logf("---------------------------------------------------\n")

	t.Log("=> [4/4] Phase 2 Dynamic Mesh assertions passed! Enterprise Mesh verified.")
}
