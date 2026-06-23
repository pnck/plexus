package effector

import (
	"context"
	"encoding/json"
	"fmt"

	"plexus/pkg/jsonschema"
)

// This file is the built-in effector FRAMEWORK only — it declares no concrete
// effector itself. The primitives live in domain files, grouped by what they
// touch: builtin_fs.go (filesystem, incl. read_file), builtin_exec.go (process
// execution, incl. run_command), builtin_sys.go (env/time/cwd) and
// builtin_mem.go (memory). Schema reflection is in schema.go.
//
// Every primitive is declared with define(spec{...}, handler): the spec states
// its identity with named fields, and the handler's argument struct IS the
// schema (reflected). No per-effector struct, no five-method boilerplate, no
// hand-written JSON schema literal.

// spec states a primitive's identity declaratively. Named fields keep each
// declaration self-documenting at the call site.
type spec struct {
	Name string  // unique tool id surfaced to the LLM
	Desc string  // model-facing description
	Risk RiskTag // side-effect tier (drives approval policy)
	// Private excludes the effector from the delegation envelope even when it is
	// approval-free — memory is private because a delegation holds none (§5.7.7).
	Private bool
}

// builtin is the single concrete Effector behind every built-in primitive.
type builtin struct {
	spec    spec
	schema  json.RawMessage
	handler func(ctx context.Context, raw json.RawMessage) (Result, error)
}

func (b *builtin) Name() string            { return b.spec.Name }
func (b *builtin) Description() string     { return b.spec.Desc }
func (b *builtin) Risk() RiskTag           { return b.spec.Risk }
func (b *builtin) AgentPrivate() bool      { return b.spec.Private }
func (b *builtin) Schema() json.RawMessage { return b.schema }
func (b *builtin) Invoke(ctx context.Context, args json.RawMessage) (Result, error) {
	return b.handler(ctx, args)
}

// define declares a built-in effector from a spec and a typed handler. The JSON
// schema is derived from the handler's argument struct T (jsonschema.For), so a
// primitive's arguments are stated once, as a Go type — never as a schema
// literal. An empty payload decodes to T's zero value; a malformed one becomes a
// tool-level error fed back to the model (not an infrastructure failure).
func define[T any](s spec, fn func(ctx context.Context, in T) (Result, error)) *builtin {
	return &builtin{
		spec:   s,
		schema: jsonschema.For[T](),
		handler: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var in T
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &in); err != nil {
					return toolErr("invalid arguments: %v", err), nil
				}
			}
			return fn(ctx, in)
		},
	}
}

// noArgs is the argument type for primitives that take no parameters.
type noArgs struct{}

// toolErr builds a tool-level error Result: the tool ran but failed. Per the
// Effector contract these return a nil Go error so the message feeds back to the
// model for self-correction rather than being treated as an infrastructure fault.
func toolErr(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}
}
