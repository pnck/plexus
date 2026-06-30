package effector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"plexus/pkg/store"
)

// Built-in memory primitives (E2.7). mem_* are backed by the agent's local
// WorkingMemory store (§5.7.9); ltm_* address the control-plane LongTermMemory,
// a NotImplemented stub at this stage. All FOUR set spec.Private: they are
// excluded from the delegation envelope because a delegation has no persistent
// memory (§5.7.7) — its job is a lean, stateless LLM↔tools loop that returns a
// distilled Result. Working memory is task-scoped via WithTaskScope
// (builtin_scope.go).

type memWriteArgs struct {
	Key     string `json:"key" desc:"Topic label."`
	Content string `json:"content" desc:"The reminder to store."`
}

// MemWrite returns the built-in mem_write effector backed by wm (RiskTag Write,
// agent-private).
func MemWrite(wm *store.WorkingMemoryStore) Effector {
	return define(spec{
		Name:    "mem_write",
		Desc:    "Save a distilled reminder to working memory under a key (recall later with mem_read). Does not enter context automatically.",
		Private: true,
	}, func(ctx context.Context, in memWriteArgs) (Result, error) {
		if wm == nil {
			return toolErr("working memory is not configured"), nil
		}
		if in.Key == "" {
			return toolErr("missing required argument: key"), nil
		}
		if err := wm.Put(ctx, taskScope(ctx), in.Key, in.Content, store.Manual); err != nil {
			return Result{}, err // infrastructure failure
		}
		return Result{Content: fmt.Sprintf("saved working memory %q", in.Key)}, nil
	})
}

type memReadArgs struct {
	Key string `json:"key,omitempty" desc:"Topic label; omit to list all."`
}

// MemRead returns the built-in mem_read effector backed by wm (RiskTag Read,
// agent-private). With a key it returns that note; without one it lists every
// note in scope.
func MemRead(wm *store.WorkingMemoryStore) Effector {
	return define(spec{
		Name:    "mem_read",
		Desc:    "Recall working memory: pass a key for that reminder, or omit it to list all reminders in scope.",
		Private: true,
	}, func(ctx context.Context, in memReadArgs) (Result, error) {
		if wm == nil {
			return toolErr("working memory is not configured"), nil
		}
		scope := taskScope(ctx)
		if in.Key != "" {
			note, err := wm.Get(ctx, scope, in.Key)
			if errors.Is(err, store.ErrNotFound) {
				return toolErr("no working memory under key %q", in.Key), nil
			}
			if err != nil {
				return Result{}, err
			}
			return Result{Content: note.Content}, nil
		}
		notes, err := wm.List(ctx, scope)
		if err != nil {
			return Result{}, err
		}
		if len(notes) == 0 {
			return Result{Content: "(working memory is empty)"}, nil
		}
		var b strings.Builder
		for _, n := range notes {
			fmt.Fprintf(&b, "%s: %s\n", n.Key, n.Content)
		}
		return Result{Content: b.String()}, nil
	})
}

// ErrLongTermMemoryNotImplemented is reported by the ltm_* effectors: long-term
// memory lives on the control plane (§5.7.9) and is not implemented yet.
var ErrLongTermMemoryNotImplemented = errors.New("long-term memory is not implemented (control-plane stub)")

type ltmGetArgs struct {
	Query string `json:"query,omitempty"`
}

// LtmGet returns the built-in ltm_get effector — a NotImplemented stub (RiskTag
// Read, agent-private).
func LtmGet() Effector {
	return define(spec{
		Name:    "ltm_get",
		Desc:    "Query long-term (cross-task) memory. NOT IMPLEMENTED yet.",
		Private: true,
	}, func(context.Context, ltmGetArgs) (Result, error) {
		return toolErr("%v", ErrLongTermMemoryNotImplemented), nil
	})
}

type ltmPutArgs struct {
	Key     string `json:"key,omitempty"`
	Content string `json:"content,omitempty"`
}

// LtmPut returns the built-in ltm_put effector — a NotImplemented stub (RiskTag
// Write, agent-private).
func LtmPut() Effector {
	return define(spec{
		Name:    "ltm_put",
		Desc:    "Store distilled cross-task experience to long-term memory. NOT IMPLEMENTED yet.",
		Private: true,
	}, func(context.Context, ltmPutArgs) (Result, error) {
		return toolErr("%v", ErrLongTermMemoryNotImplemented), nil
	})
}
