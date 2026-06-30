package effector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"plexus/pkg/mcp"
)

// fakeEffector is a minimal Effector for policy/envelope tests.
type fakeEffector struct {
	name    string
	effects EffectSet
	out     Result
	err     error
}

func (f fakeEffector) Name() string            { return f.name }
func (f fakeEffector) Description() string     { return "fake " + f.name }
func (f fakeEffector) Effects() EffectSet      { return f.effects }
func (f fakeEffector) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f fakeEffector) Invoke(context.Context, json.RawMessage) (Result, error) {
	return f.out, f.err
}

func TestDefaultPolicy(t *testing.T) {
	p := DefaultPolicy{}
	cases := []struct {
		name string
		eff  Effector
		want bool
	}{
		{"read auto-allowed", fakeEffector{name: "read_file", effects: NewEffectSet(FSRead)}, false},
		{"write auto-allowed", fakeEffector{name: "write_file", effects: NewEffectSet(FSWrite)}, false},
		{"sandboxed exec auto-allowed", fakeEffector{name: "run_tests", effects: NewEffectSet(ExecBoxed)}, false},
		{"contained build exec auto-allowed", fakeEffector{name: "build", effects: NewEffectSet(ExecBoxed)}, false},
		{"arbitrary exec requires approval", fakeEffector{name: "run_command", effects: NewEffectSet(ExecArbitrary)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.RequiresApproval(c.eff); got != c.want {
				t.Fatalf("RequiresApproval(%s)=%v want %v", c.eff.Name(), got, c.want)
			}
		})
	}
}

func TestPolicyFuncOverride(t *testing.T) {
	// An override that gates an otherwise approval-free effector, demonstrating
	// callers can supply one-off policy without declaring a type.
	p := PolicyFunc(func(e Effector) bool {
		if e.Name() == "write_secret" {
			return true
		}
		return DefaultPolicy{}.RequiresApproval(e)
	})
	if !p.RequiresApproval(fakeEffector{name: "write_secret", effects: NewEffectSet(FSWrite)}) {
		t.Fatal("override should gate write_secret")
	}
	if p.RequiresApproval(fakeEffector{name: "build", effects: NewEffectSet(ExecBoxed)}) {
		t.Fatal("sandboxed exec should remain approval-free under override")
	}
	if !p.RequiresApproval(fakeEffector{name: "run_command", effects: NewEffectSet(ExecArbitrary)}) {
		t.Fatal("arbitrary exec should still require approval")
	}
}

func TestRegistryBasics(t *testing.T) {
	r := NewRegistry(nil) // defaults to DefaultPolicy
	r.Register(fakeEffector{name: "read_file", effects: NewEffectSet(FSRead)})
	r.Register(fakeEffector{name: "run_command", effects: NewEffectSet(ExecArbitrary)})

	if _, ok := r.Get("read_file"); !ok {
		t.Fatal("expected read_file registered")
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("unexpected effector")
	}
	if got := len(r.List()); got != 2 {
		t.Fatalf("List len=%d want 2", got)
	}
	// List is sorted by name.
	if r.List()[0].Name() != "read_file" {
		t.Fatalf("List not sorted: %v", r.List()[0].Name())
	}
	if !r.RequiresApproval("run_command") {
		t.Fatal("run_command (ExecArbitrary) should require approval")
	}
	if r.RequiresApproval("read_file") {
		t.Fatal("read_file should not require approval")
	}
	// Unknown effector -> conservative approval-required.
	if !r.RequiresApproval("unknown") {
		t.Fatal("unknown effector should be treated as approval-required")
	}
}

func TestDelegationEnvelopeFiltering(t *testing.T) {
	// Filtering is purely by risk tag under DefaultPolicy — no name-matching.
	// ExecSandboxed is approval-free and INCLUDED; ExecArbitrary is gated and
	// EXCLUDED.
	r := NewRegistry(nil)
	r.Register(fakeEffector{name: "read_file", effects: NewEffectSet(FSRead)})                                       // approval-free -> included
	r.Register(fakeEffector{name: "write_scratch", effects: NewEffectSet(FSWrite)})                                  // approval-free write -> included
	r.Register(fakeEffector{name: "run_command", effects: NewEffectSet(ExecArbitrary)})                              // approval-required arbitrary exec -> excluded
	r.Register(fakeEffector{name: "contained_test", effects: NewEffectSet(ExecBoxed), out: Result{Content: "PASS"}}) // approval-free sandboxed exec -> included

	env := r.DelegationEnvelope()

	got := map[string]bool{}
	for _, e := range env.List() {
		got[e.Name()] = true
	}
	want := map[string]bool{"read_file": true, "write_scratch": true, "contained_test": true}
	if len(got) != len(want) {
		t.Fatalf("envelope set=%v want %v", got, want)
	}
	for n := range want {
		if !got[n] {
			t.Fatalf("envelope missing %q (set=%v)", n, got)
		}
	}
	if got["run_command"] {
		t.Fatalf("approval-required (ExecArbitrary) effector leaked into envelope: %v", got)
	}

	// Sandboxed exec is invocable inside the envelope (build/test self-verify).
	res, err := env.Invoke(context.Background(), "contained_test", nil)
	if err != nil {
		t.Fatalf("contained_test invoke err: %v", err)
	}
	if res.Content != "PASS" {
		t.Fatalf("contained_test result=%q", res.Content)
	}

	// Out-of-envelope invoke is denied with OutOfEnvelopeError (NOT escalation).
	_, err = env.Invoke(context.Background(), "run_command", nil)
	var oo *OutOfEnvelopeError
	if !errors.As(err, &oo) {
		t.Fatalf("expected OutOfEnvelopeError for run_command, got %v", err)
	}
	if oo.Name != "run_command" {
		t.Fatalf("OutOfEnvelopeError.Name=%q", oo.Name)
	}

	// An effector not registered at all is also denied.
	if _, err := env.Invoke(context.Background(), "ghost", nil); !errors.As(err, &oo) {
		t.Fatalf("expected OutOfEnvelopeError for ghost, got %v", err)
	}
}

func TestBuiltinReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("hi there"), 0o600); err != nil {
		t.Fatal(err)
	}
	rf := ReadFile()
	if rf.Effects() != NewEffectSet(FSRead) {
		t.Fatalf("read_file risk=%v want Read", rf.Effects())
	}
	args, _ := json.Marshal(map[string]string{"path": path})
	res, err := rf.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("invoke err: %v", err)
	}
	if res.IsError || res.Content != "hi there" {
		t.Fatalf("res=%+v", res)
	}

	// Missing file -> tool-level error (IsError), not a Go error.
	args, _ = json.Marshal(map[string]string{"path": filepath.Join(dir, "nope.txt")})
	res, err = rf.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("expected nil Go error, got %v", err)
	}
	if !res.IsError {
		t.Fatal("missing file should be tool-level error")
	}
}

func TestBuiltinRunCommand(t *testing.T) {
	rc := RunCommand()
	if rc.Effects() != NewEffectSet(ExecArbitrary) {
		t.Fatalf("run_command risk=%v want ExecArbitrary", rc.Effects())
	}
	args, _ := json.Marshal(map[string]any{"command": "echo", "args": []string{"plexus"}})
	res, err := rc.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("invoke err: %v", err)
	}
	if res.IsError {
		t.Fatalf("echo failed: %+v", res)
	}
	if res.Content != "plexus\n" {
		t.Fatalf("echo content=%q", res.Content)
	}

	// Non-zero exit -> tool-level error.
	args, _ = json.Marshal(map[string]any{"command": "false"})
	res, err = rc.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("false should yield tool-level error")
	}
}

// fakeMCPClient implements mcpCaller + mcpToolSource for adapter/registration
// tests without a live server.
type fakeMCPClient struct {
	res     mcp.ToolResult
	err     error
	tools   []mcp.ToolInfo
	listErr error
}

func (f fakeMCPClient) CallTool(context.Context, string, json.RawMessage) (mcp.ToolResult, error) {
	return f.res, f.err
}

func (f fakeMCPClient) ListTools(context.Context) ([]mcp.ToolInfo, error) {
	return f.tools, f.listErr
}

func TestMCPAdapterRiskTagging(t *testing.T) {
	risks := EffectMap{"read_doc": NewEffectSet(FSRead), "edit_doc": NewEffectSet(FSWrite)}

	// Known tools get their configured tag.
	if got := risks.EffectsFor("read_doc"); got != NewEffectSet(FSRead) {
		t.Fatalf("read_doc risk=%v want Read", got)
	}
	if got := risks.EffectsFor("edit_doc"); got != NewEffectSet(FSWrite) {
		t.Fatalf("edit_doc risk=%v want Write", got)
	}
	// Unknown tool defaults to ExecArbitrary (highest tier) for safety.
	if got := risks.EffectsFor("mystery"); got != NewEffectSet(ExecArbitrary) {
		t.Fatalf("unknown tool risk=%v want ExecArbitrary (default)", got)
	}

	// Adapter wires info -> Effector and forwards to the client.
	info := mcp.ToolInfo{Name: "mystery", Description: "?", InputSchema: json.RawMessage(`{"type":"object"}`)}
	eff := &mcpEffector{info: info, effects: risks.EffectsFor(info.Name), client: fakeMCPClient{res: mcp.ToolResult{Content: "ok"}}}
	if eff.Name() != "mystery" || eff.Effects() != NewEffectSet(ExecArbitrary) {
		t.Fatalf("adapter name/risk wrong: %s/%v", eff.Name(), eff.Effects())
	}
	res, err := eff.Invoke(context.Background(), nil)
	if err != nil || res.Content != "ok" {
		t.Fatalf("invoke res=%+v err=%v", res, err)
	}

	// Tool-level MCP error surfaces as Result.IsError.
	effErr := &mcpEffector{info: info, effects: NewEffectSet(ExecArbitrary), client: fakeMCPClient{res: mcp.ToolResult{Content: "boom", IsError: true}}}
	res, err = effErr.Invoke(context.Background(), nil)
	if err != nil {
		t.Fatalf("tool-level error should not be a Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected Result.IsError for tool-level MCP error")
	}
}

// AdaptTool wires an mcp.ToolInfo + client + EffectMap into a working Effector:
// name/description/schema pass through, the risk comes from the map (unknown ->
// ExecArbitrary), and Invoke forwards to the client.
func TestAdaptTool(t *testing.T) {
	info := mcp.ToolInfo{Name: "read_doc", Description: "read a doc", InputSchema: json.RawMessage(`{"type":"object"}`)}
	client := fakeMCPClient{res: mcp.ToolResult{Content: "DOC"}}

	eff := AdaptTool(info, client, EffectMap{"read_doc": NewEffectSet(FSRead)})
	if eff.Name() != "read_doc" {
		t.Fatalf("name=%q", eff.Name())
	}
	if eff.Description() != "read a doc" {
		t.Fatalf("description=%q", eff.Description())
	}
	if string(eff.Schema()) != `{"type":"object"}` {
		t.Fatalf("schema=%q", eff.Schema())
	}
	if eff.Effects() != NewEffectSet(FSRead) {
		t.Fatalf("risk=%v want Read", eff.Effects())
	}
	res, err := eff.Invoke(context.Background(), nil)
	if err != nil || res.Content != "DOC" {
		t.Fatalf("invoke res=%+v err=%v", res, err)
	}

	// A tool absent from the EffectMap defaults to ExecArbitrary (highest tier).
	if got := AdaptTool(mcp.ToolInfo{Name: "mystery"}, client, EffectMap{}).Effects(); got != NewEffectSet(ExecArbitrary) {
		t.Fatalf("unknown-tool risk=%v want ExecArbitrary", got)
	}

	// A transport/protocol error from the client surfaces as a Go error (distinct
	// from a tool-level Result.IsError).
	transportErr := AdaptTool(info, fakeMCPClient{err: errors.New("conn reset")}, EffectMap{})
	if _, err := transportErr.Invoke(context.Background(), nil); err == nil {
		t.Fatal("expected transport error to surface as a Go error")
	}
}

// RegisterMCPClient lists the server's tools and registers each as a risk-tagged
// Effector in the registry: tools are retrievable by name, risk tags drive the
// approval policy, Invoke flows through, and a ListTools failure propagates.
func TestRegisterMCPClient(t *testing.T) {
	client := fakeMCPClient{
		tools: []mcp.ToolInfo{
			{Name: "read_doc", Description: "r", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "danger", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		res: mcp.ToolResult{Content: "ok"},
	}
	risks := EffectMap{"read_doc": NewEffectSet(FSRead)} // "danger" is unknown -> ExecArbitrary

	reg := NewRegistry(nil)
	out, err := RegisterMCPClient(context.Background(), reg, client, risks)
	if err != nil {
		t.Fatalf("RegisterMCPClient: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("registered %d effectors, want 2", len(out))
	}

	// Both tools are registered and carry the right risk tag.
	rd, ok := reg.Get("read_doc")
	if !ok || rd.Effects() != NewEffectSet(FSRead) {
		t.Fatalf("read_doc: ok=%v risk=%v", ok, rd.Effects())
	}
	dg, ok := reg.Get("danger")
	if !ok || dg.Effects() != NewEffectSet(ExecArbitrary) {
		t.Fatalf("danger: ok=%v risk=%v want ExecArbitrary", ok, dg.Effects())
	}

	// Risk tags drive approval: ExecArbitrary gates, Read does not.
	if !reg.RequiresApproval("danger") {
		t.Fatal("danger (ExecArbitrary) should require approval")
	}
	if reg.RequiresApproval("read_doc") {
		t.Fatal("read_doc (Read) should not require approval")
	}

	// Invoke flows through the registered adapter to the client.
	if res, err := rd.Invoke(context.Background(), nil); err != nil || res.Content != "ok" {
		t.Fatalf("invoke res=%+v err=%v", res, err)
	}

	// A ListTools failure propagates as a Go error (nothing registered).
	if _, err := RegisterMCPClient(context.Background(), NewRegistry(nil), fakeMCPClient{listErr: errors.New("boom")}, risks); err == nil {
		t.Fatal("expected ListTools error to propagate")
	}
}
