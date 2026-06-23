package chat

import (
	"context"
	"errors"

	"plexus/pkg/brain"
)

// rejectEmitter denies every task_* domain event. In chat the standing task is an
// open-ended pseudo-task (no completion, no revert — see defaults.go), so
// task_report / task_revert are meaningless; rather than wiring them to a bus
// with no control-plane FSM to arbitrate, chat rejects them outright. The brain
// surfaces the rejection to the model as the tool result (§5.7.10). Real
// bus-backed delivery is a separate, deferred concern (E5 / multi-agent).
type rejectEmitter struct{}

// errTaskChannelUnavailable is returned for every emit in chat.
var errTaskChannelUnavailable = errors.New(
	"task_report/task_revert are not available in chat: the standing task is open-ended and cannot be completed or reverted")

// Emit always rejects.
func (rejectEmitter) Emit(context.Context, brain.TaskEvent) error {
	return errTaskChannelUnavailable
}
