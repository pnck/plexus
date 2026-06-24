package brain

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"plexus/pkg/effector"
	"plexus/pkg/llm"
	"plexus/protocol"
)

// ---- fake gateway ---------------------------------------------------------

// scriptedTurn is one scripted gateway response: streamed text + tool calls.
type scriptedTurn struct {
	text  string
	calls []llm.ToolCall
	err   error
}

// fakeStream is a scripted llm.EventStream emitting one DeltaText event, one
// event per tool call, and surfacing a turn error if set.
type fakeStream struct {
	events []llm.StreamEvent
	idx    int
	err    error
}

func (s *fakeStream) Next() bool {
	if s.idx >= len(s.events) {
		return false
	}
	s.idx++
	return true
}
func (s *fakeStream) Current() llm.StreamEvent { return s.events[s.idx-1] }
func (s *fakeStream) Err() error               { return s.err }
func (s *fakeStream) Close() error             { return nil }

// fakeGateway returns scripted turns in order. It can route turns to different
// scripts based on whether the latest message is a fresh-delegation system
// prompt, so a single gateway can drive both a brain and the delegation it spawns.
type fakeGateway struct {
	mu    sync.Mutex
	turns []scriptedTurn // brain script (consumed in order)
	deleg []scriptedTurn // delegation script (consumed in order)
	bi    int
	di    int
	// record captures the msgs of each call for assertions.
	calls [][]llm.Message
}

func (g *fakeGateway) GenerateStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDefinition) (llm.EventStream, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, msgs)

	isDelegation := len(msgs) > 0 && msgs[0].Role == llm.RoleSystem &&
		strings.Contains(msgs[0].Content, "isolated sub-cognition")

	var t scriptedTurn
	if isDelegation {
		if g.di >= len(g.deleg) {
			return nil, errors.New("fakeGateway: delegation script exhausted")
		}
		t = g.deleg[g.di]
		g.di++
	} else {
		if g.bi >= len(g.turns) {
			return nil, errors.New("fakeGateway: brain script exhausted")
		}
		t = g.turns[g.bi]
		g.bi++
	}
	if t.err != nil {
		return nil, t.err
	}
	var evs []llm.StreamEvent
	if t.text != "" {
		evs = append(evs, llm.StreamEvent{DeltaText: t.text})
	}
	for i := range t.calls {
		c := t.calls[i]
		evs = append(evs, llm.StreamEvent{ToolCall: &c})
	}
	evs = append(evs, llm.StreamEvent{FinishReason: "stop"})
	return &fakeStream{events: evs}, nil
}

// ---- helpers --------------------------------------------------------------

func userMsg(text string) protocol.Message {
	return protocol.Message{Type: protocol.TypeP2P, Sender: "user", Payload: []byte(text)}
}

// recordingEffector tracks invocation and returns a fixed result.
type recordingEffector struct {
	name   string
	risk   effector.RiskTag
	out    string
	called *bool
}

func (e recordingEffector) Name() string            { return e.name }
func (e recordingEffector) Description() string     { return "rec " + e.name }
func (e recordingEffector) Risk() effector.RiskTag  { return e.risk }
func (e recordingEffector) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (e recordingEffector) Invoke(context.Context, json.RawMessage) (effector.Result, error) {
	if e.called != nil {
		*e.called = true
	}
	return effector.Result{Content: e.out}, nil
}

// ---- tests ----------------------------------------------------------------

// Test 1: final-reply path ends the loop; an effector tool-call path dispatches,
// absorbs the result, loops, then converges.
func TestBrainEffectorThenFinal(t *testing.T) {
	called := false
	reg := effector.NewRegistry(nil)
	reg.Register(recordingEffector{name: "read_file", risk: effector.Read, out: "FILE-CONTENTS", called: &called})

	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"/x"}`}}},
		{text: "done: I read the file"},
	}}

	b := New(Options{Gateway: gw, Registry: reg, RoleCard: RoleCard{SystemPrompt: "you are a dev agent"}})
	out, err := b.Handle(context.Background(), userMsg("read /x please"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !called {
		t.Fatal("effector was not invoked")
	}
	if out != "done: I read the file" {
		t.Fatalf("unexpected final reply: %q", out)
	}
	// The effector result must have been absorbed as an L3 (AuthTool) frame.
	var sawToolResult bool
	for _, f := range b.History() {
		if f.Authority == protocol.AuthTool && strings.Contains(f.Content, "FILE-CONTENTS") {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatal("effector result not absorbed as an L3 tool frame")
	}
}

// Test 2: an approval-required effector triggers the Approver; a denial is fed
// back to the model and the loop continues (no crash).
func TestBrainApprovalDenied(t *testing.T) {
	reg := effector.NewRegistry(nil)
	reg.Register(recordingEffector{name: "run_command", risk: effector.ExecArbitrary, out: "ran"})

	var approvalAsked bool
	approver := FuncApprover(func(context.Context, effector.Effector, json.RawMessage) (bool, error) {
		approvalAsked = true
		return false, nil // deny
	})

	gw := &fakeGateway{turns: []scriptedTurn{
		{calls: []llm.ToolCall{{ID: "c1", Name: "run_command", Arguments: `{"command":"rm"}`}}},
		{text: "understood, I will not run it"},
	}}

	b := New(Options{Gateway: gw, Registry: reg, Approver: approver})
	out, err := b.Handle(context.Background(), userMsg("delete everything"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !approvalAsked {
		t.Fatal("approver was not consulted for an ExecArbitrary effector")
	}
	if out != "understood, I will not run it" {
		t.Fatalf("unexpected final reply: %q", out)
	}
	var sawDenied bool
	for _, f := range b.History() {
		if f.Authority == protocol.AuthTool && strings.Contains(f.Content, "DENIED") {
			sawDenied = true
		}
	}
	if !sawDenied {
		t.Fatal("denial was not fed back into history")
	}
}

// Test 3: a delegate tool call spawns a delegation; the delegation (driven by the
// same fake gateway via its delegation script) returns a Result the brain absorbs.
func TestBrainDelegateSpawnsDelegation(t *testing.T) {
	reg := effector.NewRegistry(nil)
	reg.Register(effector.ReadFile()) // a Read effector lands inside the envelope

	gw := &fakeGateway{
		turns: []scriptedTurn{
			{calls: []llm.ToolCall{{ID: "d1", Name: "delegate", Arguments: `{"objective":"survey the repo","pointers":["/etc/hostname"]}`}}},
			{text: "delegation finished; here is my summary"},
		},
		deleg: []scriptedTurn{
			// Delegation emits a distilled JSON Result and converges (no tool calls).
			{text: `{"summary":"surveyed 3 files","changes":["none"],"verified":"n/a"}`},
		},
	}

	b := New(Options{Gateway: gw, Registry: reg})
	out, err := b.Handle(context.Background(), userMsg("survey the repo"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if out != "delegation finished; here is my summary" {
		t.Fatalf("unexpected final reply: %q", out)
	}
	var sawDistilled bool
	for _, f := range b.History() {
		if f.Authority == protocol.AuthTool && strings.Contains(f.Content, "surveyed 3 files") {
			sawDistilled = true
		}
	}
	if !sawDistilled {
		t.Fatal("delegation's distilled Result was not absorbed")
	}
}

// Test 4: an out-of-envelope effector call returns *OutOfEnvelopeError and is fed
// back to the delegation's LLM (not a crash). Run the delegation loop directly so
// we also pin the structural invariant: spawnDelegation's signature takes only a
// Capabilities (no Registry, no bus) plus a max-turns bound.
func TestDelegationOutOfEnvelopeFedBack(t *testing.T) {
	reg := effector.NewRegistry(nil)
	reg.Register(effector.ReadFile())   // in-envelope (Read)
	reg.Register(effector.RunCommand()) // out-of-envelope (ExecArbitrary, approval-required)
	caps := reg.DelegationEnvelope()

	// Sanity: run_command must NOT be in the envelope.
	for _, e := range caps.List() {
		if e.Name() == "run_command" {
			t.Fatal("run_command leaked into the delegation envelope")
		}
	}

	gw := &fakeGateway{
		deleg: []scriptedTurn{
			// First the delegation tries an out-of-envelope effector.
			{calls: []llm.ToolCall{{ID: "a1", Name: "run_command", Arguments: `{"command":"ls"}`}}},
			// After the denial is fed back, it converges with a distilled Result.
			{text: `{"summary":"could not exec","open_questions":"needed run_command but not permitted"}`},
		},
	}

	var trace []string
	observe := func(s string) { trace = append(trace, s) }
	ch := spawnDelegation(context.Background(), gw, caps, Briefing{Objective: "try to exec"}, defaultDelegationMaxTurns, observe)
	r := <-ch
	if !strings.Contains(r.OpenQuestions, "not permitted") {
		t.Fatalf("delegation did not report the out-of-envelope denial: %+v", r)
	}
	// The observer (the brain's deleg-obs tap) saw the sub-cognition transcript:
	// the objective, the attempted call, and the denial fed back.
	joined := strings.Join(trace, "\n")
	if !strings.Contains(joined, "objective: try to exec") ||
		!strings.Contains(joined, "call run_command") ||
		!strings.Contains(joined, "result") {
		t.Fatalf("delegation transcript not observed: %q", trace)
	}
	// Inspect the recorded delegation messages: the denial must have been fed back
	// as a tool message before the delegation converged.
	gw.mu.Lock()
	defer gw.mu.Unlock()
	var sawDenial bool
	for _, msgs := range gw.calls {
		for _, m := range msgs {
			if m.Role == llm.RoleTool && strings.Contains(m.Content, "DENIED") {
				sawDenial = true
			}
		}
	}
	if !sawDenial {
		t.Fatal("out-of-envelope denial was not fed back to the delegation's LLM")
	}
}

// distill must never hand the parent an empty Result (§5.7.7 verify-at-boundary):
// a delegation that converges with no text still yields a non-empty distillation.
func TestDistillGuaranteesNonEmpty(t *testing.T) {
	if r := distill("   \n  "); resultEmpty(r) {
		t.Fatal("distill of blank text returned an empty Result the parent would absorb as blank")
	}
	if r := distill("did the thing"); r.Summary != "did the thing" {
		t.Fatalf("distill Summary = %q, want the text", r.Summary)
	}
	// A JSON object shaped to the ReturnSpec is used directly.
	if r := distill(`{"summary":"surveyed 3 files"}`); r.Summary != "surveyed 3 files" {
		t.Fatalf("distill JSON Summary = %q", r.Summary)
	}
}

// Test 5: authority layering — inbound from different source channels lands in
// the right L-layer.
func TestAuthorityStamping(t *testing.T) {
	cases := []struct {
		name string
		msg  protocol.Message
		want protocol.Authority
	}{
		{"user p2p -> L2 user", protocol.Message{Type: protocol.TypeP2P, Sender: "user"}, protocol.AuthUser},
		{"relay report -> L4 control", protocol.Message{Type: protocol.TypeReport, Sender: "agent-x"}, protocol.AuthControl},
		{"explicit tool -> L3", protocol.Message{Type: protocol.TypeP2P, Authority: protocol.AuthTool}, protocol.AuthTool},
		{"explicit memory -> L5", protocol.Message{Type: protocol.TypeP2P, Authority: protocol.AuthMemory}, protocol.AuthMemory},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stampAuthority(tc.msg); got != tc.want {
				t.Fatalf("stampAuthority = %v, want %v", got, tc.want)
			}
		})
	}
}

// Test: role card YAML seeds an L1 system frame, and compose renders it first.
func TestRoleCardSeedsL1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "role.yaml")
	if err := os.WriteFile(path, []byte("system_prompt: |\n  I am the Dev agent.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sp, err := LoadRoleCard(path)
	if err != nil {
		t.Fatalf("LoadRoleCard: %v", err)
	}
	if !strings.Contains(sp.SystemPrompt, "Dev agent") {
		t.Fatalf("unexpected system prompt: %q", sp.SystemPrompt)
	}

	b := New(Options{Gateway: &fakeGateway{}, RoleCard: sp})
	h := b.History()
	// L1 = kernel principles frame FIRST, then the role card frame (§5.7.11).
	if len(h) != 2 || h[0].Provenance != "principles" || h[1].Provenance != "role_card" {
		t.Fatalf("L1 not seeded as [principles, role_card]: %+v", h)
	}
	if h[0].Authority != protocol.AuthSystem || h[1].Authority != protocol.AuthSystem {
		t.Fatalf("L1 frames must be AuthSystem: %+v", h)
	}
	// compose: the kernel renders first, the role card second, both as system.
	b2 := New(Options{Gateway: &fakeGateway{}, RoleCard: sp})
	b2.history = append(b2.history, Frame{Authority: protocol.AuthUser, Role: llm.RoleUser, Content: "hello"})
	msgs := compose(b2.history)
	if msgs[0].Role != llm.RoleSystem || !strings.Contains(msgs[0].Content, "KERNEL") {
		t.Fatalf("kernel principles not rendered first: %+v", msgs[0])
	}
	if msgs[1].Role != llm.RoleSystem || !strings.Contains(msgs[1].Content, "Dev agent") {
		t.Fatalf("role card not rendered as the second system frame: %+v", msgs[1])
	}
}
