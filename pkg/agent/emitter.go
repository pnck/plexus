package agent

import (
	"context"
	"errors"

	"plexus/pkg/brain"
)

// RejectEmitter denies every task_* domain event — the default for an agent with no
// task DAG and no control-plane FSM to arbitrate task_report / task_revert. The
// brain surfaces the rejection to the model as the tool result (§5.7.10). Real
// bus-backed delivery is a separate, deferred concern (E5 / multi-agent).
type RejectEmitter struct{}

var errNoTaskChannel = errors.New(
	"task_report/task_revert are unavailable: no task channel / control-plane FSM to arbitrate them")

// Emit always rejects.
func (RejectEmitter) Emit(context.Context, brain.TaskEvent) error { return errNoTaskChannel }
