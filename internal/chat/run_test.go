package chat

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"plexus/pkg/llm"
)

// syncBuffer is a goroutine-safe writer so the REPL (main goroutine) and the
// report handler (server goroutine) can both write to the captured output.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// slowReader feeds scripted stdin lines with small gaps so the agent's async
// reports can interleave, then blocks until ctx ends (EOF would race the loop).
type slowReader struct {
	lines []string
	i     int
	gap   time.Duration
	done  <-chan struct{}
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.i >= len(r.lines) {
		<-r.done // hold the REPL open until the test cancels
		return 0, io.EOF
	}
	time.Sleep(r.gap)
	line := r.lines[r.i] + "\n"
	r.i++
	return copy(p, line), nil
}

// End-to-end CS smoke (E2.6.5/.6): Run() assembles embedded NATS + hosted agent
// + control plane + REPL; a scripted user drives a normal turn and an approval
// over the bus, and the agent's replies appear on stdout.
func TestRunEndToEndOverBus(t *testing.T) {
	gw := &fakeGateway{turns: []scriptedTurn{
		// turn 1: a plain reply.
		{text: "hi there"},
		// turn 2: an approval-gated run_command, then converge after /approve.
		{calls: []llm.ToolCall{{ID: "x1", Name: "run_command", Arguments: `{"command":"echo","args":["ok"]}`}}},
		{text: "command done"},
	}}

	done := make(chan struct{})
	in := &slowReader{
		lines: []string{"hello", "please run echo", "/approve", "/exit"},
		gap:   250 * time.Millisecond,
		done:  done,
	}
	out := &syncBuffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer close(done)

	err := Run(ctx, RunConfig{
		Gateway:           gw,
		AgentID:           "chat-agent",
		NatsPort:          freePort(t),
		IncludeRunCommand: true,
	}, in, out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := out.String()
	for _, want := range []string{"hi there", "approval required", "command done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q\n--- got ---\n%s", want, got)
		}
	}
}
