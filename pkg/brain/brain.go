package brain

import (
	"context"
	"encoding/json"
	"fmt"

	"plexus/pkg/effector"
	"plexus/pkg/llm"
	"plexus/protocol"
)

// Inbound is the brain's structured-message intake (§5.7.3). The brain receives
// STRUCTURED protocol.Messages (never raw text) and stamps each into an authority
// layer by its source channel. This is an interface so NATS wiring stays out of
// scope (E1.2/E2.6); tests feed messages directly. The brain owns the bus
// endpoint — delegations never do (§5.7.7).
type Inbound interface {
	// Recv blocks until the next structured inbound message is available, or ctx
	// is cancelled.
	Recv(ctx context.Context) (protocol.Message, error)
}

// Approver is the Yield-for-Approval seam (§5.4.3 / §5.7.4). Before running an
// effector the policy marks approval-required, the brain calls RequestApproval.
// A durable CLI/yield implementation lands later (E2.6/E1.4); here it is a plain
// interface seam with trivial implementations below.
type Approver interface {
	// RequestApproval reports whether the gated effector call may proceed. A
	// non-nil error aborts the loop; (false, nil) means denied — the brain feeds
	// the denial back to the model rather than crashing.
	RequestApproval(ctx context.Context, eff effector.Effector, args json.RawMessage) (bool, error)
}

// DenyApprover denies every approval request — a safe default seam.
type DenyApprover struct{}

// RequestApproval always denies.
func (DenyApprover) RequestApproval(context.Context, effector.Effector, json.RawMessage) (bool, error) {
	return false, nil
}

// FuncApprover adapts a function to the Approver interface, for tests and simple
// auto-approve policies.
type FuncApprover func(ctx context.Context, eff effector.Effector, args json.RawMessage) (bool, error)

// RequestApproval calls the underlying function.
func (f FuncApprover) RequestApproval(ctx context.Context, eff effector.Effector, args json.RawMessage) (bool, error) {
	return f(ctx, eff, args)
}

// Brain is an agent's SINGLETON cognition (§5.7): a role card seeded as L1, a
// layered in-memory history, a gateway, an effector Registry, a structured
// Inbound, and an Approver seam. It owns the only bus endpoint; delegations it
// spawns hold none.
type Brain struct {
	gateway            llm.Provider
	reg                *effector.Registry
	roleCard           RoleCard // structured role card; its SystemPrompt seeds an L1 frame (after the kernel)
	emitter            Emitter  // outbound seam to the control plane (task_* events, §5.7.10)
	history            []Frame  // in-memory working memory (SQLite checkpoint is E2.5)
	currentTask        string   // TaskID of the message being handled; scopes effectors and task_report
	inbound            Inbound
	approver           Approver
	maxTurns           int // cognitive-loop bound (runaway-model guard)
	delegationMaxTurns int // bound passed to each spawned delegation's loop
}

// Default loop bounds applied in New() when the corresponding Options field is 0.
const (
	defaultMaxTurns           = 32 // cognitive loop
	defaultDelegationMaxTurns = 16 // a delegation's lean LLM<->tools loop
)

// Options configures a Brain. RoleCard is the structured role card (load it with
// LoadRoleCard). Approver defaults to DenyApprover when nil. MaxTurns and
// DelegationMaxTurns default (32 / 16) when 0.
type Options struct {
	Gateway  llm.Provider
	Registry *effector.Registry
	RoleCard RoleCard
	Inbound  Inbound
	Approver Approver
	// Emitter is the outbound seam to the control plane for task_* domain events
	// (§5.7.10). Defaults to NopEmitter (drops events) when nil — the real
	// JetStream/control-plane impl lands with E1.2/E5.
	Emitter Emitter
	// MaxTurns bounds the brain's cognitive loop against a runaway model.
	// Defaults to 32 when 0.
	MaxTurns int
	// DelegationMaxTurns bounds each spawned delegation's lean LLM<->tools loop.
	// Defaults to 16 when 0.
	DelegationMaxTurns int
}

// New constructs a Brain and seeds its history with the role card as the L1
// system frame (§5.7.2: role-card content rendered into the highest layer).
func New(opt Options) *Brain {
	app := opt.Approver
	if app == nil {
		app = DenyApprover{}
	}
	maxTurns := opt.MaxTurns
	if maxTurns == 0 {
		maxTurns = defaultMaxTurns
	}
	delegationMaxTurns := opt.DelegationMaxTurns
	if delegationMaxTurns == 0 {
		delegationMaxTurns = defaultDelegationMaxTurns
	}
	emitter := opt.Emitter
	if emitter == nil {
		emitter = NopEmitter{}
	}
	b := &Brain{
		gateway:            opt.Gateway,
		reg:                opt.Registry,
		roleCard:           opt.RoleCard,
		emitter:            emitter,
		inbound:            opt.Inbound,
		approver:           app,
		maxTurns:           maxTurns,
		delegationMaxTurns: delegationMaxTurns,
	}
	// L1 kernel: the built-in operating principles seed the highest layer FIRST
	// (§5.7.11), so the universal invariants precede the role card's
	// specialization. compose() renders all system frames in order.
	b.history = append(b.history, Frame{
		Authority:  protocol.AuthSystem,
		Provenance: "principles",
		Role:       llm.RoleSystem,
		Content:    principlesPrompt,
	})
	if opt.RoleCard.SystemPrompt != "" {
		b.history = append(b.history, Frame{
			Authority:  protocol.AuthSystem,
			Provenance: "role_card",
			Role:       llm.RoleSystem,
			Content:    opt.RoleCard.SystemPrompt,
		})
	}
	return b
}

// History returns a snapshot of the brain's current history frames. Exposed so a
// checkpoint layer (E2.5) and tests can inspect what would be persisted; the
// brain itself keeps history in memory.
func (b *Brain) History() []Frame {
	out := make([]Frame, len(b.history))
	copy(out, b.history)
	return out
}

// Step receives one structured inbound message, stamps it into the right
// authority layer, and runs the cognitive loop to convergence, returning the
// final reply text. This is the §5.7.8 flowchart minus the (E2.5) SQLite
// checkpoint, whose insertion point is marked below.
func (b *Brain) Step(ctx context.Context) (string, error) {
	msg, err := b.inbound.Recv(ctx)
	if err != nil {
		return "", err
	}
	// ① stamp authority by source channel (Sender → L-layer, §5.7.3).
	b.currentTask = msg.TaskID
	b.history = append(b.history, frameFromInbound(msg))
	return b.run(ctx)
}

// Handle injects an already-constructed inbound message (bypassing Inbound) and
// runs the loop. Useful for tests and for callers that already hold the message.
func (b *Brain) Handle(ctx context.Context, msg protocol.Message) (string, error) {
	b.currentTask = msg.TaskID
	b.history = append(b.history, frameFromInbound(msg))
	return b.run(ctx)
}

// run is the cognitive loop (§5.7.8): compose layered context -> gateway stream
// -> parse intent -> dispatch (final / effector / delegate) -> absorb -> loop.
// The loop is bounded by the brain's configured MaxTurns (runaway-model guard).
func (b *Brain) run(ctx context.Context) (string, error) {
	tools := b.toolSurface()
	for turn := 0; turn < b.maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		// ② compose() renders frames into a layered message list.
		msgs := compose(b.history)
		// ③ gateway stream + ④ parse StreamEvents. (No mode gate — plexus has none.)
		text, calls, err := stream(ctx, b.gateway, msgs, tools)
		if err != nil {
			return "", err
		}
		// ⑤ decide intent.
		if len(calls) == 0 {
			// Final reply (no tool calls) -> converge. Record it and emit.
			b.history = append(b.history, Frame{
				// assistant's own output — not an input-trust layer; compose keys off Role.
				Provenance: "assistant",
				Role:       llm.RoleAssistant,
				Content:    text,
			})
			// ⑦ CHECKPOINT POINT: history would be persisted to SQLite here (E2.5).
			return text, nil
		}
		// Record the assistant's tool-call turn so the wire history is well-formed.
		b.history = append(b.history, Frame{
			// assistant's tool-call turn — not an input-trust layer; compose keys off Role.
			Provenance: "assistant",
			Role:       llm.RoleAssistant,
			Content:    text,
			ToolCalls:  calls,
		})
		// Dispatch each call, absorbing the result as an L3/L4 frame. Effectors run
		// under the current task's scope (§5.7.10) so task-scoped tools — working
		// memory and the step_* checkpoint primitives — address the right task.
		dctx := effector.WithTaskScope(ctx, b.currentTask)
		for _, call := range calls {
			content := b.dispatch(dctx, call)
			b.history = append(b.history, Frame{
				Authority:  protocol.AuthTool, // ⑥ tool/delegation results are L3 data
				Provenance: call.Name,
				Role:       llm.RoleTool,
				Content:    content,
				ToolCallID: call.ID,
			})
		}
		// ⑦ CHECKPOINT POINT: history would be persisted to SQLite here (E2.5),
		// then the loop re-composes and continues.
	}
	return "", fmt.Errorf("brain hit max turns (%d) without converging", b.maxTurns)
}

// toolSurface is the set of tools surfaced to the LLM: every registered effector
// PLUS the built-in delegate tool (§5.7.8). The brain intercepts delegate; all
// other names route to effectors.
func (b *Brain) toolSurface() []llm.ToolDefinition {
	var defs []llm.ToolDefinition
	if b.reg != nil {
		defs = toolDefs(b.reg.List())
	}
	defs = append(defs, llm.ToolDefinition{
		Name:        delegateToolName,
		Description: "Delegate a self-contained, flood-producing sub-task to a fresh isolated delegation. It runs with a curated capability envelope and returns ONLY a distilled result. Use when the work is self-containable via a briefing; inline trivial work yourself.",
		Parameters:  delegateSchema(),
	})
	// Brain-owned task channel tools (§5.7.10): these emit domain events to the
	// control plane via the bus, which the brain owns — so they are brain tools,
	// not effectors, and never reach a delegation's envelope.
	defs = append(defs, taskToolDefs()...)
	return defs
}

// dispatch routes one tool call to its handler and returns the model-facing
// result content. A delegate call spawns a fresh delegation; any other name is an
// effector call gated by the approval hook.
func (b *Brain) dispatch(ctx context.Context, call llm.ToolCall) string {
	switch call.Name {
	case delegateToolName:
		return b.delegate(ctx, call)
	case taskReportToolName, taskRevertToolName:
		return b.emitTask(ctx, call)
	default:
		return b.runEffector(ctx, call)
	}
}

// runEffector dispatches an effector call: it consults the policy's approval
// requirement, invokes the Approver hook when gated, and runs or refuses
// accordingly. A denial is fed back to the model (not a crash).
func (b *Brain) runEffector(ctx context.Context, call llm.ToolCall) string {
	if b.reg == nil {
		return fmt.Sprintf("no effector registry configured; cannot run %q", call.Name)
	}
	eff, ok := b.reg.Get(call.Name)
	if !ok {
		return fmt.Sprintf("unknown tool %q", call.Name)
	}
	args := json.RawMessage(call.Arguments)
	// Yield-for-Approval point (§5.7.8 / §5.7.4).
	if b.reg.RequiresApproval(call.Name) {
		ok, err := b.approver.RequestApproval(ctx, eff, args)
		if err != nil {
			return fmt.Sprintf("approval check for %q errored: %v", call.Name, err)
		}
		if !ok {
			return fmt.Sprintf("DENIED: %q requires human approval and it was not granted. Do not retry; either choose a different approach or report that human approval is needed.", call.Name)
		}
	}
	res, err := eff.Invoke(ctx, args)
	if err != nil {
		return fmt.Sprintf("effector %q failed (infrastructure): %v", call.Name, err)
	}
	if res.IsError {
		return "tool error: " + res.Content
	}
	return res.Content
}

// delegate intercepts a delegate tool call: it builds a Briefing from the args,
// takes the delegation capability envelope from the registry, spawns a FRESH
// delegation (bounded by the brain's configured DelegationMaxTurns), and waits
// for its distilled Result (or ctx cancellation). The Result is rendered to text
// and absorbed by the caller as the tool result — a distillation, never the
// child's transcript (§5.7.7).
func (b *Brain) delegate(ctx context.Context, call llm.ToolCall) string {
	var a delegateArgs
	if err := json.Unmarshal([]byte(call.Arguments), &a); err != nil {
		return fmt.Sprintf("invalid delegate arguments: %v", err)
	}
	if a.Objective == "" {
		return "delegate requires a non-empty objective"
	}
	var caps effector.Capabilities = emptyEnvelope{}
	if b.reg != nil {
		caps = b.reg.DelegationEnvelope() // curated 能力封套 — NOT the full registry
	}
	ch := spawnDelegation(ctx, b.gateway, caps, a.briefing(), b.delegationMaxTurns)
	select {
	case r := <-ch:
		return renderResult(r)
	case <-ctx.Done():
		return fmt.Sprintf("delegation cancelled: %v", ctx.Err())
	}
}

// renderResult formats a distilled Result for absorption into the brain's
// history. Encoding as compact JSON keeps it structured and machine-stable while
// remaining a distillation, never a transcript.
func renderResult(r Result) string {
	data, err := json.Marshal(r)
	if err != nil {
		return r.Summary
	}
	return string(data)
}

// emptyEnvelope is a Capabilities with nothing permitted, used when a brain has
// no registry. Every Invoke is out-of-envelope.
type emptyEnvelope struct{}

func (emptyEnvelope) List() []effector.Effector { return nil }
func (emptyEnvelope) Invoke(_ context.Context, name string, _ json.RawMessage) (effector.Result, error) {
	return effector.Result{}, &effector.OutOfEnvelopeError{Name: name}
}
