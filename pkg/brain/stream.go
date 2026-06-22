package brain

import (
	"context"

	"plexus/pkg/effector"
	"plexus/pkg/llm"
)

// stream drives one gateway generation to completion, accumulating streamed text
// and collecting any tool calls. It consumes StreamEvents per §5.7.8 ⑤: DeltaText
// chunks are concatenated, ToolCalls are collected, and the stream is drained to
// FinishReason. A stream-level error (event.Error or stream.Err) is returned so
// the caller can decide how to surface it. Used by both the brain loop and the
// delegation loop.
func stream(ctx context.Context, gateway llm.Provider, msgs []llm.Message, tools []llm.ToolDefinition) (text string, calls []llm.ToolCall, err error) {
	es, err := gateway.GenerateStream(ctx, msgs, tools)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = es.Close() }()

	var sb []byte
	for es.Next() {
		ev := es.Current()
		if ev.Error != nil {
			return string(sb), calls, ev.Error
		}
		if ev.DeltaText != "" {
			sb = append(sb, ev.DeltaText...)
		}
		if ev.ToolCall != nil {
			calls = append(calls, *ev.ToolCall)
		}
	}
	if e := es.Err(); e != nil {
		return string(sb), calls, e
	}
	return string(sb), calls, nil
}

// toolDefs converts a slice of effectors into LLM tool definitions (name /
// description / JSON schema). Used to surface both the registry's effectors to
// the brain and the envelope's effectors to a delegation.
func toolDefs(effs []effector.Effector) []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(effs))
	for _, e := range effs {
		defs = append(defs, llm.ToolDefinition{
			Name:        e.Name(),
			Description: e.Description(),
			Parameters:  e.Schema(),
		})
	}
	return defs
}
