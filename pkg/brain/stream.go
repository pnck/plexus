package brain

import (
	"context"

	"plexus/pkg/effector"
	"plexus/pkg/llm"
)

// stream drives one gateway generation to completion, accumulating streamed text
// and collecting any tool calls. It consumes StreamEvents per §5.7.8 ⑤: DeltaText
// chunks are concatenated, ToolCalls are collected, and the stream is drained to
// FinishReason. If onDelta is non-nil it is called with each text chunk as it
// arrives (live display); pass nil to disable. Token usage from the terminal
// event is returned. A stream-level error (event.Error or stream.Err) is returned
// so the caller can decide how to surface it. Used by both the brain loop and the
// delegation loop.
func stream(ctx context.Context, gateway llm.Provider, msgs []llm.Message, tools []llm.ToolDefinition, onDelta, onThinking func(string)) (text string, calls []llm.ToolCall, reasoning []llm.ReasoningBlock, usage llm.Usage, err error) {
	es, err := gateway.GenerateStream(ctx, msgs, tools)
	if err != nil {
		return "", nil, nil, usage, err
	}
	defer func() { _ = es.Close() }()

	var sb []byte
	for es.Next() {
		ev := es.Current()
		if ev.Error != nil {
			return string(sb), calls, reasoning, usage, ev.Error
		}
		if ev.DeltaText != "" {
			sb = append(sb, ev.DeltaText...)
			if onDelta != nil {
				onDelta(ev.DeltaText)
			}
		}
		// Thinking is shown live but never accumulated into the answer text, so it
		// does not enter history (it is a draft, §5.7.9). The completed, SIGNED
		// block is captured separately for verbatim replay on a tool-call turn —
		// it is opaque attestation, not readable context.
		if ev.DeltaThinking != "" && onThinking != nil {
			onThinking(ev.DeltaThinking)
		}
		if ev.ReasoningBlock != nil {
			reasoning = append(reasoning, *ev.ReasoningBlock)
		}
		if ev.ToolCall != nil {
			calls = append(calls, *ev.ToolCall)
		}
		if ev.Usage != nil {
			usage = *ev.Usage
		}
	}
	if e := es.Err(); e != nil {
		return string(sb), calls, reasoning, usage, e
	}
	return string(sb), calls, reasoning, usage, nil
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
