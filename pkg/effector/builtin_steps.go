package effector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"plexus/pkg/store"
)

// Built-in checkpoint/step primitives (§5.7.9 "checkpoints as steps", §5.7.10).
// They drive the agent's OWN plan: the ordered Checkpoint chain is both the plan
// (each Goal a step) and the resume point. Like mem_*, they are local-SQLite,
// agent-private effectors (spec.Private) backed by a CheckpointStore and scoped
// to the current task (WithTaskScope) — a delegation has no checkpoints. Note:
// these mutate the agent's PRIVATE plan only; changing a control-plane task's
// truth goes through the brain's task_* events (§5.7.10), never these.
//
// suspend/resume are intentionally NOT exposed: suspension is driven by the yield
// mechanism (§5.7.5), not freely chosen by the model.

// stepErr maps a CheckpointStore error to model-facing feedback.
func stepErr(action string, seq int64, err error) Result {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return toolErr("no step #%d", seq)
	case errors.Is(err, store.ErrConflict):
		return toolErr("step #%d is not in a state that allows %s", seq, action)
	default:
		return Result{Content: fmt.Sprintf("step %s failed: %v", action, err), IsError: true}
	}
}

type stepAddArgs struct {
	Goal string `json:"goal" desc:"What this step accomplishes (human-facing)."`
}

// StepAdd returns the built-in step_add effector (Write, agent-private): append a
// new step (goal) to the end of the plan.
func StepAdd(cs *store.CheckpointStore) Effector {
	return define(spec{
		Name:    "step_add",
		Desc:    "Add a step (goal) to the end of your plan; returns its seq. The ordered chain is your plan.",
		Private: true,
	}, func(ctx context.Context, in stepAddArgs) (Result, error) {
		if cs == nil {
			return toolErr("checkpoint store not configured"), nil
		}
		if in.Goal == "" {
			return toolErr("missing required argument: goal"), nil
		}
		cp, err := cs.Append(ctx, taskScope(ctx), in.Goal)
		if err != nil {
			return Result{}, err // infrastructure failure
		}
		return Result{Content: fmt.Sprintf("added step #%d: %s", cp.Seq, cp.Goal)}, nil
	})
}

type stepSeqArgs struct {
	Seq int64 `json:"seq" desc:"The step's sequence number."`
}

// StepStart returns the built-in step_start effector (Write, agent-private):
// activate a pending step (or wake a suspended one).
func StepStart(cs *store.CheckpointStore) Effector {
	return define(spec{
		Name:    "step_start",
		Desc:    "Mark a step active (begin working it).",
		Private: true,
	}, func(ctx context.Context, in stepSeqArgs) (Result, error) {
		if cs == nil {
			return toolErr("checkpoint store not configured"), nil
		}
		if err := cs.Activate(ctx, taskScope(ctx), in.Seq); err != nil {
			return stepErr("start", in.Seq, err), nil
		}
		return Result{Content: fmt.Sprintf("started step #%d", in.Seq)}, nil
	})
}

type stepCompleteArgs struct {
	Seq    int64  `json:"seq" desc:"The step's sequence number."`
	Result string `json:"result,omitempty" desc:"Distilled outcome of the step (feeds the next step / resume)."`
}

// StepComplete returns the built-in step_complete effector (Write, agent-private):
// mark an active step done with its distilled result.
func StepComplete(cs *store.CheckpointStore) Effector {
	return define(spec{
		Name:    "step_complete",
		Desc:    "Mark an active step done, recording its distilled result.",
		Private: true,
	}, func(ctx context.Context, in stepCompleteArgs) (Result, error) {
		if cs == nil {
			return toolErr("checkpoint store not configured"), nil
		}
		if err := cs.Complete(ctx, taskScope(ctx), in.Seq, in.Result); err != nil {
			return stepErr("complete", in.Seq, err), nil
		}
		return Result{Content: fmt.Sprintf("completed step #%d", in.Seq)}, nil
	})
}

// StepBlock returns the built-in step_block effector (Write, agent-private): mark
// an active step blocked.
func StepBlock(cs *store.CheckpointStore) Effector {
	return define(spec{
		Name:    "step_block",
		Desc:    "Mark an active step blocked.",
		Private: true,
	}, func(ctx context.Context, in stepSeqArgs) (Result, error) {
		if cs == nil {
			return toolErr("checkpoint store not configured"), nil
		}
		if err := cs.Block(ctx, taskScope(ctx), in.Seq); err != nil {
			return stepErr("block", in.Seq, err), nil
		}
		return Result{Content: fmt.Sprintf("blocked step #%d", in.Seq)}, nil
	})
}

// StepList returns the built-in step_list effector (Read, agent-private): read the
// plan — the ordered step chain with each step's status and goal.
func StepList(cs *store.CheckpointStore) Effector {
	return define(spec{
		Name:    "step_list",
		Desc:    "List your plan: the ordered steps with their status and goal.",
		Private: true,
	}, func(ctx context.Context, _ noArgs) (Result, error) {
		if cs == nil {
			return toolErr("checkpoint store not configured"), nil
		}
		steps, err := cs.Steps(ctx, taskScope(ctx))
		if err != nil {
			return Result{}, err
		}
		if len(steps) == 0 {
			return Result{Content: "(no steps yet)"}, nil
		}
		var b strings.Builder
		for _, s := range steps {
			fmt.Fprintf(&b, "#%d [%s] %s", s.Seq, s.Status, s.Goal)
			if s.Result != "" {
				fmt.Fprintf(&b, " — %s", s.Result)
			}
			b.WriteByte('\n')
		}
		return Result{Content: b.String()}, nil
	})
}
