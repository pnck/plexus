package brain

import (
	"context"
	"encoding/json"
	"fmt"

	"plexus/pkg/jsonschema"
	"plexus/pkg/llm"
)

// The task channel (§5.7.10). A `task` is owned by the control plane, not the
// agent: the brain never mutates task truth directly — it EMITS a domain event
// and the control-plane FSM decides. These are brain-intercepted tools (like
// delegate), not effectors, because emission rides the bus, which the brain
// alone owns; they therefore never reach a delegation's capability envelope
// (§5.7.7). The transport (JetStream, E1.2/E1.3) and the arbiter (control-plane
// FSM, E5) are deferred — Emitter is the seam, NopEmitter the placeholder impl.

// TaskEventKind distinguishes the task domain events the brain emits.
type TaskEventKind int

const (
	TaskReportEvent TaskEventKind = iota // progress/status update
	TaskRevertEvent                      // request to reopen a task reported done in error
)

func (k TaskEventKind) String() string {
	switch k {
	case TaskReportEvent:
		return "task_report"
	case TaskRevertEvent:
		return "task_revert"
	default:
		return fmt.Sprintf("TaskEventKind(%d)", int(k))
	}
}

// TaskEvent is an outbound domain event the brain emits to the control plane
// (§5.7.10). It is a REQUEST: the control-plane FSM decides legality and any
// cascade to dependent DAG nodes; the agent does not own task truth and must not
// assume the event took effect.
type TaskEvent struct {
	Kind    TaskEventKind
	TaskID  string // target task (for revert, may be one previously reported done)
	Status  string // task_report: the new status
	Summary string // task_report: short progress summary
	Reason  string // task_revert: why it must be reopened
}

// Emitter is the brain's outbound seam to the control plane (§5.7.10). A real
// implementation maps the event to a protocol domain event (E1.1 reserved range)
// and publishes it over JetStream (E1.2/E1.3) for the control-plane FSM (E5) to
// arbitrate. Until that exists, NopEmitter drops events.
type Emitter interface {
	Emit(ctx context.Context, ev TaskEvent) error
}

// NopEmitter drops every event — the default seam before the bus exists.
type NopEmitter struct{}

// Emit discards the event.
func (NopEmitter) Emit(context.Context, TaskEvent) error { return nil }

const (
	taskReportToolName = "task_report"
	taskRevertToolName = "task_revert"
)

type taskReportArgs struct {
	Status  string `json:"status" desc:"New task status, e.g. in_progress, done, blocked."`
	Summary string `json:"summary,omitempty" desc:"Short progress summary."`
}

type taskRevertArgs struct {
	TaskID string `json:"task_id,omitempty" desc:"Task to reopen; defaults to the current task. May be one previously reported done."`
	Reason string `json:"reason" desc:"Why the task must be reverted/reopened."`
}

// taskToolDefs returns the LLM tool definitions for the task channel, surfaced by
// the brain alongside delegate (never inside a delegation envelope).
func taskToolDefs() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{
			Name:        taskReportToolName,
			Description: "Report this task's progress/status to the control plane (a domain event, not a local edit).",
			Parameters:  jsonschema.For[taskReportArgs](),
		},
		{
			Name:        taskRevertToolName,
			Description: "Request the control plane reopen a task whose reported state is wrong (e.g. previously reported done but not actually done). This is a REQUEST — the control plane decides; do not assume it is reverted.",
			Parameters:  jsonschema.For[taskRevertArgs](),
		},
	}
}

// emitTask handles a task_report / task_revert tool call: it builds the domain
// event and emits it via the seam, returning request-semantics feedback to the
// model — it states the event was EMITTED, never that the task was changed.
func (b *Brain) emitTask(ctx context.Context, call llm.ToolCall) string {
	switch call.Name {
	case taskReportToolName:
		var a taskReportArgs
		if err := json.Unmarshal([]byte(call.Arguments), &a); err != nil {
			return fmt.Sprintf("invalid task_report arguments: %v", err)
		}
		if a.Status == "" {
			return "task_report requires a status"
		}
		ev := TaskEvent{Kind: TaskReportEvent, TaskID: b.currentTask, Status: a.Status, Summary: a.Summary}
		if err := b.emitter.Emit(ctx, ev); err != nil {
			return fmt.Sprintf("task_report emit failed: %v", err)
		}
		return fmt.Sprintf("emitted task_report(status=%q) for task %q to the control plane.", a.Status, b.currentTask)

	case taskRevertToolName:
		var a taskRevertArgs
		if err := json.Unmarshal([]byte(call.Arguments), &a); err != nil {
			return fmt.Sprintf("invalid task_revert arguments: %v", err)
		}
		if a.Reason == "" {
			return "task_revert requires a reason"
		}
		target := a.TaskID
		if target == "" {
			target = b.currentTask
		}
		ev := TaskEvent{Kind: TaskRevertEvent, TaskID: target, Reason: a.Reason}
		if err := b.emitter.Emit(ctx, ev); err != nil {
			return fmt.Sprintf("task_revert emit failed: %v", err)
		}
		return fmt.Sprintf("emitted task_revert REQUEST for task %q (reason: %s). The control plane decides whether and how to reopen it — do NOT assume it is reverted.", target, a.Reason)

	default:
		return fmt.Sprintf("unknown task tool %q", call.Name)
	}
}
