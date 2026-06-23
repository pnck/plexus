package chat

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"plexus/pkg/llm"
	"plexus/pkg/mesh"
	"plexus/protocol"
	"plexus/server"
)

// ---- bus test harness ----

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// busFixture stands up an embedded NATS + a control plane that captures the
// agent's frames, and hosts an agent driven by gw.
type busFixture struct {
	srv     *server.Server
	frames  chan inFrame
	agentID string
	host    *Host
	url     string
}

func startBus(t *testing.T, gw llm.Provider, opts ...func(*Config)) *busFixture {
	t.Helper()
	port := freePort(t)
	ns, err := server.StartEmbeddedNATS(port, t.TempDir())
	if err != nil {
		t.Fatalf("embedded nats: %v", err)
	}
	t.Cleanup(func() { ns.Shutdown(); ns.WaitForShutdown() })
	url := "nats://127.0.0.1:" + strconv.Itoa(port)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fx := &busFixture{frames: make(chan inFrame, 64), agentID: "chat-agent", url: url}
	fx.srv = server.New(server.WithNatsURL(url), server.WithOnReport(func(m protocol.Message) {
		if f, ok := decodeFrame(m.Payload); ok {
			fx.frames <- inFrame{f: f, corr: m.CorrelationID}
		}
	}))
	go func() { _ = fx.srv.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	cfg := Config{Gateway: gw, DBPath: ":memory:"}
	for _, o := range opts {
		o(&cfg)
	}
	h, err := NewHost(ctx, fx.agentID, cfg, mesh.WithNatsURL(url))
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	fx.host = h
	go func() { _ = h.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for !registered(fx.srv, fx.agentID) {
		if time.Now().After(deadline) {
			t.Fatal("agent did not register")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fx
}

func registered(srv *server.Server, id string) bool {
	for _, a := range srv.GetRegisteredAgents() {
		if a == id {
			return true
		}
	}
	return false
}

func (fx *busFixture) say(t *testing.T, corr, text string) {
	t.Helper()
	fx.send(t, corr, Frame{Kind: kindSay, Text: text}, DefaultTaskID)
}

func (fx *busFixture) ctrl(t *testing.T, corr, cmd, arg string) {
	t.Helper()
	fx.send(t, corr, Frame{Kind: kindCtrl, Cmd: cmd, Arg: arg}, "")
}

func (fx *busFixture) send(t *testing.T, corr string, f Frame, task string) {
	t.Helper()
	if err := fx.srv.SendP2P(context.Background(), fx.agentID, protocol.Message{
		CorrelationID: corr, TaskID: task, Payload: encodeFrame(f),
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// collectUntil reads frames until one of kind is seen, returning ALL frames read
// (so a test can also inspect earlier frames like deltas/usage without losing
// them). Fails on timeout.
func (fx *busFixture) collectUntil(t *testing.T, kind string) []inFrame {
	t.Helper()
	var got []inFrame
	timeout := time.After(3 * time.Second)
	for {
		select {
		case in := <-fx.frames:
			got = append(got, in)
			if in.f.Kind == kind {
				return got
			}
		case <-timeout:
			t.Fatalf("timeout waiting for %q frame (got %d frames)", kind, len(got))
		}
	}
}

// await reads frames until one of kind is seen and returns it, without
// discarding the frames read before it (collectUntil buffers them; this picks
// the matching one). Convenient when a test only needs the terminal frame.
func (fx *busFixture) await(t *testing.T, kind string) inFrame {
	t.Helper()
	return find(t, fx.collectUntil(t, kind), kind)
}

// find returns the first frame of kind in the slice, or fails.
func find(t *testing.T, fs []inFrame, kind string) inFrame {
	t.Helper()
	for _, in := range fs {
		if in.f.Kind == kind {
			return in
		}
	}
	t.Fatalf("no %q frame among %d collected", kind, len(fs))
	return inFrame{}
}

// ---- tests ----

func TestBusSayStreamsAndReplies(t *testing.T) {
	gw := &fakeGateway{turns: []scriptedTurn{{text: "hello back"}}}
	fx := startBus(t, gw)
	fx.say(t, "t1", "hello")

	fs := fx.collectUntil(t, kindReply)
	// A streamed delta carried the text.
	if d := find(t, fs, kindDelta); d.f.Text != "hello back" {
		t.Fatalf("delta = %q", d.f.Text)
	}
	// The reply terminator is paired to the turn.
	rep := find(t, fs, kindReply)
	if !rep.f.Done || rep.corr != "t1" || rep.f.Text != "hello back" {
		t.Fatalf("reply = %+v", rep)
	}
}

func TestBusApprovalApprove(t *testing.T) {
	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "x1", Name: "run_command", Arguments: `{"command":"echo","args":["hi"]}`}}},
		{text: "ran it"},
	}}
	fx := startBus(t, gw, func(c *Config) { c.IncludeRunCommand = true })
	fx.say(t, "t1", "run echo")

	req := fx.await(t, kindApproval)
	if !strings.Contains(req.f.Text, "run_command") {
		t.Fatalf("approval desc = %q", req.f.Text)
	}
	fx.send(t, req.corr, Frame{Kind: kindAnswer, Text: approveWord}, "")

	rep := fx.await(t, kindReply)
	if rep.f.Text != "ran it" {
		t.Fatalf("reply = %q", rep.f.Text)
	}
}

func TestBusApprovalDeny(t *testing.T) {
	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "x1", Name: "run_command", Arguments: `{"command":"rm"}`}}},
		{text: "won't run it"},
	}}
	fx := startBus(t, gw, func(c *Config) { c.IncludeRunCommand = true })
	fx.say(t, "t1", "delete things")

	req := fx.await(t, kindApproval)
	fx.send(t, req.corr, Frame{Kind: kindAnswer, Text: denyWord}, "")

	rep := fx.await(t, kindReply)
	if rep.f.Text != "won't run it" {
		t.Fatalf("reply = %q", rep.f.Text)
	}
	var sawDenied bool
	for _, f := range fx.host.agent.Brain.History() {
		if f.Authority == protocol.AuthTool && strings.Contains(f.Content, "DENIED") {
			sawDenied = true
		}
	}
	if !sawDenied {
		t.Fatal("denial not fed back to the brain")
	}
}

// A tool call streams an always-on activity marker the moment it starts (so the
// user sees the agent working), before the final reply — no /trace needed.
func TestBusToolActivityShown(t *testing.T) {
	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "x1", Name: "step_add", Arguments: `{"goal":"do the thing"}`}}}, // Write = auto-allowed
		{text: "added"},
	}}
	fx := startBus(t, gw)
	fx.say(t, "t1", "plan it")

	act := fx.await(t, kindActivity)
	if !strings.Contains(act.f.Text, "step_add") {
		t.Fatalf("activity = %q, want the tool name", act.f.Text)
	}
	if rep := fx.await(t, kindReply); rep.f.Text != "added" {
		t.Fatalf("reply = %q", rep.f.Text)
	}
}

func TestBusControlToolsAndStatus(t *testing.T) {
	fx := startBus(t, &fakeGateway{})

	fx.ctrl(t, "c1", cmdTools, "")
	r := fx.await(t, kindCtrl)
	if !strings.Contains(r.f.Text, "read_file") {
		t.Fatalf("/tools missing read_file: %q", r.f.Text)
	}
	if strings.Contains(r.f.Text, "delegate") {
		t.Fatalf("/tools should not list delegate (a brain tool, not an effector): %q", r.f.Text)
	}
	// A fixed (non-mutable) gateway reports as such.
	fx.ctrl(t, "c2", cmdStatus, "")
	if r := fx.await(t, kindCtrl); !strings.Contains(r.f.Text, "fixed") {
		t.Fatalf("/status (fixed gateway) = %q", r.f.Text)
	}
}

func TestBusControlReset(t *testing.T) {
	gw := &fakeGateway{turns: []scriptedTurn{{text: "hi"}}}
	fx := startBus(t, gw)
	fx.say(t, "t1", "hello")
	_ = fx.await(t, kindReply)

	// History has grown beyond the 2 seed frames.
	if n := len(fx.host.agent.Brain.History()); n <= 2 {
		t.Fatalf("history len before reset = %d, want > 2", n)
	}
	fx.ctrl(t, "c1", cmdReset, "")
	r := fx.await(t, kindCtrl)
	if !strings.Contains(r.f.Text, "cleared") {
		t.Fatalf("/reset = %q", r.f.Text)
	}
	if n := len(fx.host.agent.Brain.History()); n != 2 {
		t.Fatalf("history len after reset = %d, want 2 (kernel + role card)", n)
	}
}

// thinkingStream emits a thinking delta, then an answer delta, then finishes.
type thinkingStream struct {
	evs []llm.StreamEvent
	i   int
}

func (s *thinkingStream) Next() bool               { s.i++; return s.i <= len(s.evs) }
func (s *thinkingStream) Current() llm.StreamEvent { return s.evs[s.i-1] }
func (s *thinkingStream) Err() error               { return nil }
func (s *thinkingStream) Close() error             { return nil }

type thinkingGateway struct{}

func (thinkingGateway) GenerateStream(context.Context, []llm.Message, []llm.ToolDefinition) (llm.EventStream, error) {
	return &thinkingStream{evs: []llm.StreamEvent{
		{DeltaThinking: "let me reason… "},
		{DeltaThinking: "ok."},
		{DeltaText: "the answer"},
		{FinishReason: "stop"},
	}}, nil
}

// Thinking is streamed inline as kindThinking, mirrored once to sys.obs.<id>.
// thinking, and never enters the brain's history (it is a draft).
func TestBusThinkingStreamAndObs(t *testing.T) {
	fx := startBus(t, thinkingGateway{})

	// Watch the obs.thinking stream like `plexus watch` would.
	nc, err := nats.Connect(fx.url)
	if err != nil {
		t.Fatalf("nats: %v", err)
	}
	t.Cleanup(nc.Close)
	obs := make(chan string, 4)
	_, _ = nc.Subscribe("sys.obs."+fx.agentID+".thinking", func(m *nats.Msg) {
		var msg protocol.Message
		if json.Unmarshal(m.Data, &msg) == nil {
			obs <- string(msg.Payload)
		}
	})
	time.Sleep(100 * time.Millisecond)

	fx.say(t, "t1", "think about it")
	fs := fx.collectUntil(t, kindReply)

	// Thinking arrived as kindThinking frames; the answer as kindDelta.
	var think, answer string
	for _, in := range fs {
		switch in.f.Kind {
		case kindThinking:
			think += in.f.Text
		case kindDelta:
			answer += in.f.Text
		}
	}
	if think != "let me reason… ok." {
		t.Fatalf("thinking frames = %q", think)
	}
	if answer != "the answer" {
		t.Fatalf("answer frames = %q", answer)
	}

	// One obs.thinking mirror per turn carries the full reasoning.
	select {
	case line := <-obs:
		if line != "let me reason… ok." {
			t.Fatalf("obs.thinking = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no obs.thinking emitted")
	}

	// Thinking must NOT be in history; only the answer is.
	for _, f := range fx.host.agent.Brain.History() {
		if strings.Contains(f.Content, "let me reason") {
			t.Fatalf("thinking leaked into history: %q", f.Content)
		}
	}
}

// blockingGateway streams nothing until ctx is cancelled, then errors — to
// exercise interrupting a turn mid-generation.
type blockingGateway struct{ next llm.Provider }

func (g *blockingGateway) GenerateStream(ctx context.Context, msgs []llm.Message, tools []llm.ToolDefinition) (llm.EventStream, error) {
	// First call (the turn we interrupt) blocks; later calls delegate to next.
	if g.next != nil {
		return g.next.GenerateStream(ctx, msgs, tools)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// Ctrl-C (a cancel frame) resets ONLY the in-flight turn: the turn returns
// "[interrupted]" and the agent stays alive to serve the next turn.
func TestBusInterruptResetsTurnNotAgent(t *testing.T) {
	// turn 1 blocks (interrupted); turn 2 uses a real fake reply.
	gw := &blockingGateway{}
	fx := startBus(t, gw)

	fx.say(t, "t1", "do something slow")
	// Give the worker a moment to enter the blocked gateway call.
	time.Sleep(200 * time.Millisecond)
	fx.send(t, "t1", Frame{Kind: kindCancel}, "")

	rep := fx.await(t, kindReply)
	if rep.f.Text != "[interrupted]" {
		t.Fatalf("interrupted reply = %q, want [interrupted]", rep.f.Text)
	}

	// The agent is still alive: swap in a working gateway and a second turn works.
	gw.next = &fakeGateway{turns: []scriptedTurn{{text: "still here"}}}
	fx.say(t, "t2", "are you alive?")
	rep2 := find(t, fx.collectUntil(t, kindReply), kindReply)
	if rep2.f.Text != "still here" {
		t.Fatalf("post-interrupt reply = %q, want 'still here' (agent died?)", rep2.f.Text)
	}
}

// Tool/delegation trace is published to the observability subject
// (sys.obs.<id>.trace), OFF the functional report channel. A wildcard subscriber
// (as `plexus watch` or chat /trace does) sees every tool call; the report
// channel carries no trace.
func TestBusTraceOnObsSubject(t *testing.T) {
	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "s1", Name: "step_add", Arguments: `{"goal":"plan"}`}}},
		{text: "noted"},
	}}
	fx := startBus(t, gw)

	// Subscribe to the obs stream like a watcher would.
	nc, err := nats.Connect(fx.url)
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	obs := make(chan string, 16)
	sub, err := nc.Subscribe("sys.obs."+fx.agentID+".>", func(m *nats.Msg) {
		var msg protocol.Message
		if json.Unmarshal(m.Data, &msg) == nil {
			obs <- m.Subject + " | " + string(msg.Payload)
		}
	})
	if err != nil {
		t.Fatalf("obs subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	time.Sleep(100 * time.Millisecond) // let the subscription register

	fx.say(t, "t1", "make a plan")

	// The functional report stream carries the reply but NO trace.
	for _, in := range fx.collectUntil(t, kindReply) {
		if in.f.Kind == kindTrace {
			t.Fatalf("trace leaked onto the report channel: %+v", in.f)
		}
	}
	// The obs stream carries the tool trace.
	select {
	case line := <-obs:
		if !strings.Contains(line, "sys.obs.chat-agent.trace") || !strings.Contains(line, "step_add") || !strings.Contains(line, "added step #0") {
			t.Fatalf("obs trace = %q, want sys.obs…trace step_add → added step #0", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no trace on the obs subject")
	}
}

// A runtime-reconfigurable gateway starts unconfigured: a turn reports the
// missing key, and /key + /status reflect the change.
func TestBusUnconfiguredGatewayAndKey(t *testing.T) {
	gw := NewMutableGateway(GatewayConfig{Provider: "openai", Model: "gpt-4o-mini"}) // no key
	fx := startBus(t, gw)

	fx.ctrl(t, "c1", cmdStatus, "")
	if r := fx.await(t, kindCtrl); !strings.Contains(r.f.Text, "UNCONFIGURED") {
		t.Fatalf("/status before key = %q", r.f.Text)
	}

	fx.say(t, "t1", "hello")
	if e := fx.await(t, kindError); !strings.Contains(e.f.Text, "no LLM key") {
		t.Fatalf("turn error = %q", e.f.Text)
	}

	fx.ctrl(t, "c2", cmdKey, "sk-test")
	if r := fx.await(t, kindCtrl); !strings.Contains(r.f.Text, "api key set") {
		t.Fatalf("/key = %q", r.f.Text)
	}
	fx.ctrl(t, "c3", cmdStatus, "")
	if r := fx.await(t, kindCtrl); !strings.Contains(r.f.Text, "state=ready") {
		t.Fatalf("/status after key = %q", r.f.Text)
	}

	// /reasoning accepts a superset tier (max); /status reflects it; off clears it.
	fx.ctrl(t, "c4", cmdReasoning, "max")
	if r := fx.await(t, kindCtrl); !strings.Contains(r.f.Text, "max") {
		t.Fatalf("/reasoning max = %q", r.f.Text)
	}
	// No-arg /reasoning is a GET — it reports the current tier, never sets off.
	fx.ctrl(t, "c4b", cmdReasoning, "")
	if r := fx.await(t, kindCtrl); !strings.Contains(r.f.Text, "reasoning = max") {
		t.Fatalf("/reasoning (no arg) = %q, want current tier", r.f.Text)
	}
	fx.ctrl(t, "c5", cmdStatus, "")
	if r := fx.await(t, kindCtrl); !strings.Contains(r.f.Text, "reasoning=max") {
		t.Fatalf("/status reasoning = %q", r.f.Text)
	}
	// An unknown tier is rejected with usage.
	fx.ctrl(t, "c6", cmdReasoning, "bananas")
	if r := fx.await(t, kindCtrl); !strings.Contains(r.f.Text, "usage:") {
		t.Fatalf("/reasoning bananas = %q, want usage", r.f.Text)
	}
	fx.ctrl(t, "c7", cmdReasoning, "off")
	if r := fx.await(t, kindCtrl); !strings.Contains(r.f.Text, "off") {
		t.Fatalf("/reasoning off = %q", r.f.Text)
	}
}
