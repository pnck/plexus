package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"plexus/pkg/effector"
	"plexus/pkg/llm"
)

// spawnDelegation starts a fresh delegation sub-cognition and returns a channel
// that yields its single distilled Result (§5.7.7). This is the brain's SOLE
// spawn primitive — fresh only (fork is a control-plane operation, continue is
// cut, per §5.7.7 / E2.4).
//
// The signature is the structural enforcement of the hard invariants: the
// delegation receives ONLY a gateway, a capability envelope (caps — the mediated
// 能力封套, NOT the Registry), a Briefing, and a max-turns bound. It therefore
// has NO nats.Conn, NO Registry, NO role card, NO Inbound, NO persistent memory,
// and NO way to spawn further delegations. Its context is fresh: history is
// seeded solely by the briefing system prompt. Cancelling ctx kills the
// goroutine; its only exit is the returned channel.
func spawnDelegation(ctx context.Context, gateway llm.Provider, caps effector.Capabilities, b Briefing, maxTurns int) <-chan Result {
	ch := make(chan Result, 1)
	go func() {
		ch <- runDelegation(ctx, gateway, caps, b, maxTurns)
		// Goroutine exits; the delegation is dead after one Result.
	}()
	return ch
}

// runDelegation is the lean LLM<->tools loop. It renders the briefing into a cold
// system prompt, drives the gateway with tools = caps.List() (the envelope), runs
// tool calls via caps.Invoke (out-of-envelope -> *OutOfEnvelopeError fed back to
// the model, never a crash), loops up to maxTurns, and finally distills the
// model's last text into a Result. maxTurns bounds the loop so a misbehaving
// model cannot spin forever; the brain treats a hit bound as a distilled (if
// partial) Result.
func runDelegation(ctx context.Context, gateway llm.Provider, caps effector.Capabilities, b Briefing, maxTurns int) Result {
	history := []llm.Message{{Role: llm.RoleSystem, Content: briefingPrompt(b)}}
	tools := toolDefs(caps.List())

	var lastText string
	for turn := 0; turn < maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return Result{Summary: "delegation cancelled", OpenQuestions: err.Error()}
		}
		text, calls, err := stream(ctx, gateway, history, tools)
		if err != nil {
			return Result{Summary: "delegation gateway error", OpenQuestions: err.Error()}
		}
		if text != "" {
			lastText = text
		}
		if len(calls) == 0 {
			// Final turn: distill the last text into a Result.
			return distill(lastText)
		}
		// Record the assistant's tool-call turn, then answer each call.
		history = append(history, llm.Message{Role: llm.RoleAssistant, Content: text, ToolCalls: calls})
		for _, call := range calls {
			content := invokeEnvelope(ctx, caps, call)
			history = append(history, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    content,
			})
		}
	}
	return Result{
		Summary:       distill(lastText).Summary,
		OpenQuestions: fmt.Sprintf("delegation hit max turns (%d) without converging", maxTurns),
	}
}

// invokeEnvelope runs one tool call through the capability envelope, translating
// an out-of-envelope request into model-facing feedback rather than a crash.
func invokeEnvelope(ctx context.Context, caps effector.Capabilities, call llm.ToolCall) string {
	res, err := caps.Invoke(ctx, call.Name, json.RawMessage(call.Arguments))
	if err != nil {
		var oo *effector.OutOfEnvelopeError
		if errors.As(err, &oo) {
			// The delegation has no inbound and cannot escalate: it is told the tool
			// is outside its envelope and should report the need in its Result.
			return fmt.Sprintf("DENIED: %v. You cannot escalate; if this capability is required, report it in your final result under open questions.", oo)
		}
		// Infrastructure error: surface it so the model can adapt.
		return fmt.Sprintf("tool %q failed: %v", call.Name, err)
	}
	if res.IsError {
		return "tool error: " + res.Content
	}
	return res.Content
}

// distill parses the delegation's final text into a Result. The delegation is
// prompted to shape its answer to the ReturnSpec; if it emitted a JSON object
// matching the Result fields we use it directly, otherwise the whole text becomes
// the Summary. Either way the parent receives a distillation, never a transcript.
func distill(text string) Result {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") {
		var r Result
		if err := json.Unmarshal([]byte(trimmed), &r); err == nil && !resultEmpty(r) {
			return r
		}
	}
	return Result{Summary: trimmed}
}

// resultEmpty reports whether a Result carries no distilled content (Result has a
// slice field, so it is not directly comparable).
func resultEmpty(r Result) bool {
	return r.Summary == "" && len(r.Changes) == 0 && r.Verified == "" &&
		r.Decisions == "" && r.OpenQuestions == ""
}
