package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"plexus/pkg/effector"
	"plexus/pkg/llm"
	"plexus/pkg/store"
	"plexus/protocol"
)

// YieldError is returned by Handle/Resume when the cognitive loop suspended a step
// awaiting an external answer (the durable Yield-for-Approval of §5.7.5). The
// caller (the bus host) emits the ask tagged with Corr, parks, and later calls
// Resume(corr, granted) when the answer arrives over the durable inbox. It is a
// control signal, not a failure — the synchronous Approver path never produces it.
type YieldError struct {
	Corr        string // CorrelationID the suspended step waits on (answer pairs back on it)
	Description string // human-facing ask (e.g. "run_command wants to run: …")
	TaskID      string // the task whose step is suspended
}

func (e *YieldError) Error() string {
	return fmt.Sprintf("brain yielded on task %s awaiting answer %s", e.TaskID, e.Corr)
}

// pendingYield captures the in-memory turn state a live brain needs to resume a
// suspended step precisely (run the gated call, then the rest of the turn). It is
// lost when the process dies — a fresh brain instead rebuilds from the persisted
// step chain (resumeFromCheckpoints), per §5.7.9 ("rebuild from the step chain,
// never by replaying history").
type pendingYield struct {
	corr   string
	taskID string
	calls  []llm.ToolCall // the assistant turn's full tool-call list
	idx    int            // the gated call we suspended at
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
// layered in-memory history, a gateway, an effector Registry, and an Approver
// seam. It owns the only bus endpoint; delegations it spawns hold none.
type Brain struct {
	gateway            llm.Provider
	reg                *effector.Registry
	roleCard           RoleCard // structured role card; RenderSystemPrompt() seeds an L1 frame (after the kernel)
	emitter            Emitter  // outbound seam to the control plane (task_* events, §5.7.10)
	history            []Frame  // in-memory working transcript; the durable plan lives in the CheckpointStore (§5.7.9)
	currentTask        string   // TaskID of the message being handled; scopes effectors and task_report
	approver           Approver
	onDelta            func(string)                    // optional: live streamed-text sink (nil = off)
	onThinking         func(string)                    // optional: live reasoning/thinking sink (nil = off)
	onUsage            func(llm.Usage)                 // optional: per-turn token usage sink (nil = off)
	onToolStart        func(name, args string)         // optional: BEFORE each tool/delegation dispatch (live activity)
	onTool             func(name, args, result string) // optional: AFTER each tool/delegation dispatch (observability)
	onDelegTrace       func(string)                    // optional: a spawned delegation's transcript lines (observability)
	maxTurns           int                             // cognitive-loop bound (runaway-model guard)
	delegationMaxTurns int                             // bound passed to each spawned delegation's loop

	// Durable yield/resume (§5.7.5). When yieldForApproval is set (the bus host
	// enables it), a gated effector suspends a checkpoint and returns a YieldError
	// instead of blocking on the Approver — so the wait survives process death and
	// resumes from the persisted step chain. checkpoints is the store the brain
	// suspends/activates on; newCorrID mints the correlation id a yield waits on.
	checkpoints      *store.CheckpointStore
	yieldForApproval bool
	newCorrID        func() string
	pending          *pendingYield // in-memory state for a precise (live) resume; nil after death
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
	// Checkpoints is the brain-private step store (§5.7.9). Required for durable
	// yield/resume; when set together with YieldForApproval, a gated effector
	// suspends a step rather than blocking the Approver.
	Checkpoints *store.CheckpointStore
	// YieldForApproval switches the approval mechanism from the synchronous
	// Approver (blocks the loop) to durable yield: the brain suspends a checkpoint
	// and returns a *YieldError, to be woken by Resume when the answer arrives over
	// the durable inbox. Requires Checkpoints. The in-process Agent and unit tests
	// leave it false and keep the Approver path.
	YieldForApproval bool
	// NewCorrID mints the correlation id a yield waits on (injected to avoid a
	// time/random dependency). Defaults to a monotonic counter when nil.
	NewCorrID func() string
	// OnDelta, if set, receives streamed assistant-text chunks as they arrive
	// during the cognitive loop (live display). Called on the loop's goroutine.
	OnDelta func(string)
	// OnThinking, if set, receives streamed reasoning/thinking chunks for live
	// display. Thinking is shown but never enters history (it is a draft).
	OnThinking func(string)
	// OnUsage, if set, receives the accumulated token usage once per turn, just
	// before the final reply is returned.
	OnUsage func(llm.Usage)
	// OnToolStart, if set, is called immediately BEFORE each tool/delegation
	// dispatch with the tool name and its JSON arguments. It exists so a UI can
	// show activity the moment work begins — crucial for long-running delegation,
	// whose result (via OnTool) lands only after the sub-task completes.
	OnToolStart func(name, args string)
	// OnTool, if set, is called after each tool/delegation dispatch with the tool
	// name, its JSON arguments, and the result fed back to the model — a pure
	// observability tap (the result still flows into history regardless).
	OnTool func(name, args, result string)
	// OnDelegTrace, if set, receives a spawned delegation's transcript line by
	// line (each turn's assistant text, tool calls, and results). The delegation
	// itself has no bus endpoint (§5.7.7); the brain — which DOES — taps it on the
	// delegation's behalf, so this preserves the invariant while making the
	// otherwise-invisible sub-cognition observable (sys.obs.<id>.deleg).
	OnDelegTrace func(string)
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
	newCorrID := opt.NewCorrID
	if newCorrID == nil {
		var n int
		newCorrID = func() string { n++; return fmt.Sprintf("yield-%d", n) }
	}
	b := &Brain{
		gateway:            opt.Gateway,
		reg:                opt.Registry,
		roleCard:           opt.RoleCard,
		emitter:            emitter,
		approver:           app,
		onDelta:            opt.OnDelta,
		onThinking:         opt.OnThinking,
		onUsage:            opt.OnUsage,
		onToolStart:        opt.OnToolStart,
		onTool:             opt.OnTool,
		onDelegTrace:       opt.OnDelegTrace,
		maxTurns:           maxTurns,
		delegationMaxTurns: delegationMaxTurns,
		checkpoints:        opt.Checkpoints,
		yieldForApproval:   opt.YieldForApproval && opt.Checkpoints != nil,
		newCorrID:          newCorrID,
	}
	b.seed()
	return b
}

// seed (re)initializes history with the L1 system frames: the built-in kernel
// principles FIRST (§5.7.11), then the role card — so the universal invariants
// precede the role's specialization. compose() renders system frames in order.
// Not concurrency-safe: callers must run it on the loop's goroutine.
func (b *Brain) seed() {
	b.history = b.history[:0]
	b.history = append(b.history, Frame{
		Authority:  protocol.AuthSystem,
		Provenance: "principles",
		Role:       llm.RoleSystem,
		Content:    principlesPrompt,
	})
	if rendered := b.roleCard.RenderSystemPrompt(); rendered != "" {
		b.history = append(b.history, Frame{
			Authority:  protocol.AuthSystem,
			Provenance: "role_card",
			Role:       llm.RoleSystem,
			Content:    rendered,
		})
	}
}

// Reset clears the conversation and re-seeds L1 (kernel + current role card) —
// the /reset command. Not concurrency-safe: call it from the goroutine that
// drives the cognitive loop (the bus host serializes it with turns).
func (b *Brain) Reset() { b.seed() }

// SetRoleCard replaces the role card and resets — the /system command. Same
// single-goroutine constraint as Reset.
func (b *Brain) SetRoleCard(rc RoleCard) {
	b.roleCard = rc
	b.seed()
}

// RoleCard returns the current role card. Read-only; used by the no-arg /system
// command to show the active system prompt without mutating history.
func (b *Brain) RoleCard() RoleCard { return b.roleCard }

// History returns a snapshot of the brain's current history frames. Exposed so
// the checkpoint layer and tests can inspect what would be persisted; the brain
// itself keeps history in memory.
func (b *Brain) History() []Frame {
	out := make([]Frame, len(b.history))
	copy(out, b.history)
	return out
}

// Handle stamps a structured inbound message into its authority layer (§5.7.3)
// and runs the cognitive loop to convergence, returning the final reply text.
// It is the brain's single intake: the host pushes messages off the bus and
// calls Handle; there is no separate pull loop.
func (b *Brain) Handle(ctx context.Context, msg protocol.Message) (string, error) {
	// ① stamp authority by source channel (Sender → L-layer, §5.7.3).
	b.currentTask = msg.TaskID
	b.history = append(b.history, frameFromInbound(msg))
	return b.run(ctx)
}

// run is the cognitive loop (§5.7.8): compose layered context -> gateway stream
// -> parse intent -> dispatch (final / effector / delegate) -> absorb -> loop.
// The loop is bounded by the brain's configured MaxTurns (runaway-model guard).
func (b *Brain) run(ctx context.Context) (string, error) {
	tools := b.toolSurface()
	var turnUsage llm.Usage
	for turn := 0; turn < b.maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		// ② compose() renders frames into a layered message list.
		msgs := compose(b.history)
		// ③ gateway stream + ④ parse StreamEvents. (No mode gate — plexus has none.)
		text, calls, reasoning, usage, err := stream(ctx, b.gateway, msgs, tools, b.onDelta, b.onThinking)
		if err != nil {
			return "", err
		}
		turnUsage.PromptTokens += usage.PromptTokens
		turnUsage.CompletionTokens += usage.CompletionTokens
		turnUsage.TotalTokens += usage.TotalTokens
		// ⑤ decide intent.
		if len(calls) == 0 {
			// Final reply (no tool calls) -> converge. Record it and emit.
			b.history = append(b.history, Frame{
				// assistant's own output — not an input-trust layer; compose keys off Role.
				Provenance: "assistant",
				Role:       llm.RoleAssistant,
				Content:    text,
			})
			if b.onUsage != nil {
				b.onUsage(turnUsage)
			}
			// ⑦ §5.7.9: there is no history-as-checkpoint. The step plan IS the
			// checkpoint chain and the agent already persists it via the step_*
			// tools (CheckpointStore); history frames are the step's droppable
			// working transcript. Cross-process resume rebuilds from the step chain
			// + working memory (persistent yield: E1.4), never by replaying history.
			return text, nil
		}
		// Record the assistant's tool-call turn so the wire history is well-formed.
		// Reasoning blocks are kept ONLY on this tool-call turn (the only place a
		// provider needs them replayed); they are opaque attestation, never shown
		// and never composed as readable context.
		b.history = append(b.history, Frame{
			// assistant's tool-call turn — not an input-trust layer; compose keys off Role.
			Provenance: "assistant",
			Role:       llm.RoleAssistant,
			Content:    text,
			ToolCalls:  calls,
			Reasoning:  reasoning,
		})
		// Dispatch the turn's calls. In yield mode a gated call suspends a step and
		// returns a *YieldError up to the caller (the loop unwinds; Resume continues
		// it later); otherwise the loop re-composes and continues.
		if ye := b.runCalls(ctx, calls, 0); ye != nil {
			return "", ye
		}
		// ⑦ §5.7.9: nothing to checkpoint here — the step plan persists via the
		// step_* tools (CheckpointStore); history is the droppable working
		// transcript. The loop re-composes and continues.
	}
	return "", fmt.Errorf("brain hit max turns (%d) without converging", b.maxTurns)
}

// runCalls dispatches calls[start:], absorbing each result as an L3 tool frame.
// Effectors run under the current task's scope (§5.7.10) so task-scoped tools —
// working memory and the step_* checkpoint primitives — address the right task.
// In yield mode, a gated effector call suspends a checkpoint (WaitFor = a fresh
// correlation id), stashes the turn's remaining state for a precise live resume,
// and returns a *YieldError — the durable Yield-for-Approval point (§5.7.5).
func (b *Brain) runCalls(ctx context.Context, calls []llm.ToolCall, start int) *YieldError {
	dctx := effector.WithTaskScope(ctx, b.currentTask)
	for i := start; i < len(calls); i++ {
		call := calls[i]
		if b.yieldForApproval && b.needsApproval(call) {
			ye, err := b.suspendForApproval(ctx, calls, i)
			if err != nil {
				// Could not persist the suspension — fail the call back to the model
				// rather than silently dropping the gate.
				b.absorbToolResult(call, fmt.Sprintf("approval could not be requested: %v", err))
				continue
			}
			return ye
		}
		if b.onToolStart != nil {
			b.onToolStart(call.Name, call.Arguments)
		}
		content := b.dispatch(dctx, call)
		if b.onTool != nil {
			b.onTool(call.Name, call.Arguments, content)
		}
		b.absorbToolResult(call, content)
	}
	return nil
}

// needsApproval reports whether a call routes to an approval-gated effector. The
// delegate and task_* tools are brain-owned and never gated here.
func (b *Brain) needsApproval(call llm.ToolCall) bool {
	switch call.Name {
	case DelegateToolName, taskReportToolName, taskRevertToolName:
		return false
	default:
		return b.reg != nil && b.reg.RequiresApproval(call.Name)
	}
}

// suspendForApproval persists the yield: it ensures the current task has an Active
// step, suspends it on a fresh correlation id, stashes the turn state for a live
// resume, and returns the YieldError describing the ask.
func (b *Brain) suspendForApproval(ctx context.Context, calls []llm.ToolCall, idx int) (*YieldError, error) {
	call := calls[idx]
	corr := b.newCorrID()
	seq, err := b.ensureActiveStep(ctx, "awaiting approval: "+call.Name)
	if err != nil {
		return nil, err
	}
	if err := b.checkpoints.Suspend(ctx, b.currentTask, seq, corr); err != nil {
		return nil, err
	}
	b.pending = &pendingYield{corr: corr, taskID: b.currentTask, calls: calls, idx: idx}
	return &YieldError{
		Corr:        corr,
		Description: approvalDescription(call),
		TaskID:      b.currentTask,
	}, nil
}

// ensureActiveStep returns the seq of the current task's Active step, creating one
// (Append + Activate) with the given goal if none is active yet — so a yield
// always has a concrete step to suspend even when the model has not planned steps.
func (b *Brain) ensureActiveStep(ctx context.Context, goal string) (int64, error) {
	if cp, ok, err := b.checkpoints.Active(ctx, b.currentTask); err != nil {
		return 0, err
	} else if ok {
		return cp.Seq, nil
	}
	cp, err := b.checkpoints.Append(ctx, b.currentTask, goal)
	if err != nil {
		return 0, err
	}
	if err := b.checkpoints.Activate(ctx, b.currentTask, cp.Seq); err != nil {
		return 0, err
	}
	return cp.Seq, nil
}

// absorbToolResult appends a tool/delegation result to history as an L3 data frame
// (⑥ tool results are L3, never instructions).
func (b *Brain) absorbToolResult(call llm.ToolCall, content string) {
	b.history = append(b.history, Frame{
		Authority:  protocol.AuthTool,
		Provenance: call.Name,
		Role:       llm.RoleTool,
		Content:    content,
		ToolCallID: call.ID,
	})
}

// approvalDescription renders the human-facing ask for a gated call.
func approvalDescription(call llm.ToolCall) string {
	return fmt.Sprintf("%s wants to run: %s", call.Name, call.Arguments)
}

// Resume wakes a step suspended by a yield (§5.7.5): it activates the checkpoint(s)
// waiting on corr and continues the cognitive loop. A live brain (the same process
// that suspended) resumes precisely — it runs the gated call with the granted/denied
// decision and finishes the turn. A fresh brain (the suspending process died; this
// one has only seed history) instead rebuilds working context from the persisted
// step chain and re-enters, per §5.7.9. Resume may itself yield again.
func (b *Brain) Resume(ctx context.Context, corr string, granted bool) (string, error) {
	if b.checkpoints == nil {
		return "", fmt.Errorf("brain has no checkpoint store; cannot resume")
	}
	waiters, err := b.checkpoints.Waiting(ctx, corr)
	if err != nil {
		return "", err
	}
	if len(waiters) == 0 {
		return "", fmt.Errorf("no suspended step waits on %q", corr)
	}
	for _, cp := range waiters {
		if err := b.checkpoints.Activate(ctx, cp.TaskID, cp.Seq); err != nil {
			return "", err
		}
	}
	// Precise live resume: same process, in-memory turn state intact.
	if b.pending != nil && b.pending.corr == corr {
		py := b.pending
		b.pending = nil
		b.currentTask = py.taskID
		gated := py.calls[py.idx]
		content := b.decideGatedCall(ctx, gated, granted)
		if b.onTool != nil {
			b.onTool(gated.Name, gated.Arguments, content)
		}
		b.absorbToolResult(gated, content)
		if ye := b.runCalls(ctx, py.calls, py.idx+1); ye != nil {
			return "", ye
		}
		return b.run(ctx)
	}
	// Rebuild resume: a fresh brain reconstructs context from the step chain.
	return b.resumeFromCheckpoints(ctx, waiters[0].TaskID, granted)
}

// decideGatedCall runs the previously-gated effector if approval was granted, or
// returns the standing denial message if not (fed back to the model, never a crash).
func (b *Brain) decideGatedCall(ctx context.Context, call llm.ToolCall, granted bool) string {
	if !granted {
		return fmt.Sprintf("DENIED: %q requires human approval and it was not granted. Do not retry; either choose a different approach or report that human approval is needed.", call.Name)
	}
	eff, ok := b.reg.Get(call.Name)
	if !ok {
		return fmt.Sprintf("unknown tool %q", call.Name)
	}
	dctx := effector.WithTaskScope(ctx, b.currentTask)
	res, err := eff.Invoke(dctx, json.RawMessage(call.Arguments))
	if err != nil {
		return fmt.Sprintf("effector %q failed (infrastructure): %v", call.Name, err)
	}
	if res.IsError {
		return "tool error: " + res.Content
	}
	return res.Content
}

// resumeFromCheckpoints rebuilds a fresh brain's working context from the task's
// persisted step chain and re-enters the cognitive loop (§5.7.9: resume rebuilds
// from steps + working memory, never by replaying history). The granted decision
// for the step that was awaiting approval is folded into the rebuilt context as L2
// user-authority data so the model continues from the human's answer.
func (b *Brain) resumeFromCheckpoints(ctx context.Context, taskID string, granted bool) (string, error) {
	b.currentTask = taskID
	steps, err := b.checkpoints.Steps(ctx, taskID)
	if err != nil {
		return "", err
	}
	b.seed() // kernel + role card only — the in-memory transcript died with the process
	b.history = append(b.history, Frame{
		Authority:  protocol.AuthUser, // the resume briefing carries the human's answer (L2)
		Provenance: "resume",
		Role:       llm.RoleUser,
		Content:    rebuildBriefing(taskID, steps, granted),
	})
	return b.run(ctx)
}

// rebuildBriefing renders the resume context from the step chain: completed steps
// (goal + distilled result) for continuity, the active step that was awaiting the
// answer, and the human's granted/denied decision.
func rebuildBriefing(taskID string, steps []store.Checkpoint, granted bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Resuming task %q after an interruption. Progress so far (from the durable step chain):\n", taskID)
	for _, s := range steps {
		fmt.Fprintf(&sb, "- step %d [%s] %s", s.Seq, s.Status, s.Goal)
		if s.Result != "" {
			fmt.Fprintf(&sb, " => %s", s.Result)
		}
		sb.WriteString("\n")
	}
	decision := "GRANTED"
	if !granted {
		decision = "DENIED"
	}
	fmt.Fprintf(&sb, "The action you were waiting on for the active step was %s by the human. Continue the task from here.", decision)
	return sb.String()
}

// AsYield extracts a *YieldError from err, reporting whether the loop suspended.
// Callers use it to tell a durable yield apart from an ordinary failure.
func AsYield(err error) (*YieldError, bool) {
	var ye *YieldError
	if errors.As(err, &ye) {
		return ye, true
	}
	return nil, false
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
		Name:        DelegateToolName,
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
	case DelegateToolName:
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
	ch := spawnDelegation(ctx, b.gateway, caps, a.briefing(), b.delegationMaxTurns, b.onDelegTrace)
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
