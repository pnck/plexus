package effector

import "context"

// Task scope (§5.7.9/§5.7.10). Working memory and the checkpoint/step primitives
// are BOTH scoped to the agent's current task — the scope value is the TaskID.
// The brain binds it with WithTaskScope before dispatching effector calls; absent
// it, task-scoped effectors fall back to defaultScope so they remain usable in
// isolation (tests, ad-hoc).

const defaultScope = "default"

type taskScopeKey struct{}

// WithTaskScope binds the current task id as the scope for task-scoped effectors.
func WithTaskScope(ctx context.Context, taskID string) context.Context {
	return context.WithValue(ctx, taskScopeKey{}, taskID)
}

func taskScope(ctx context.Context) string {
	if v, ok := ctx.Value(taskScopeKey{}).(string); ok && v != "" {
		return v
	}
	return defaultScope
}
