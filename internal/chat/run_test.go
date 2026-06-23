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

// syncBuffer is a goroutine-safe writer (readline + the frame reader both write).
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

// scriptReader feeds scripted stdin lines paced so async replies interleave, then
// blocks until the test ends (so the REPL stays open between lines).
type scriptReader struct {
	lines []string
	i     int
	gap   time.Duration
	done  <-chan struct{}
}

func (r *scriptReader) Read(p []byte) (int, error) {
	if r.i >= len(r.lines) {
		<-r.done
		return 0, io.EOF
	}
	time.Sleep(r.gap)
	line := r.lines[r.i] + "\n"
	r.i++
	return copy(p, line), nil
}

// End-to-end CS smoke through Run(): a scripted user sends a turn, runs a control
// command, triggers an approval and approves it, then exits — all over the bus,
// with the rich REPL client. Asserts the agent's streamed reply and control
// output reach stdout.
func TestRunEndToEnd(t *testing.T) {
	gw := &fakeGateway{turns: []scriptedTurn{
		{text: "hello there"},
		{calls: []llm.ToolCall{{ID: "x1", Name: "run_command", Arguments: `{"command":"echo","args":["ok"]}`}}},
		{text: "ran the command"},
	}}

	done := make(chan struct{})
	in := &scriptReader{
		lines: []string{"hello", "/tools", "please run echo", "/approve", "/exit"},
		gap:   300 * time.Millisecond,
		done:  done,
	}
	out := &syncBuffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	defer close(done)

	if err := Run(ctx, RunConfig{
		Gateway:           gw, // scripted fake drives the turns
		AgentID:           "chat-agent",
		NatsPort:          freePort(t),
		IncludeRunCommand: true,
	}, in, out); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := out.String()
	for _, want := range []string{"hello there", "read_file", "approval required", "ran the command"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q\n--- got ---\n%s", want, got)
		}
	}
}
