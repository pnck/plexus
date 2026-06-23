// Package chat is the cohesive home of the `plexus chat` product: a fully
// assembled agent (brain + effector + delegation + memory + checkpoint) and the
// chat-specific pieces that make it a runnable conversational agent — the
// default role card and standing task, the task-channel reject policy, and (in
// later E2.6 nodes) the CS host, interactive approver and REPL client. The
// generic framework stays in pkg/*; only the chat-specific defaults live here.
package chat

import "plexus/pkg/brain"

// DefaultTaskID is the standing pseudo-task every chat turn is scoped to. plexus
// has no session concept; a chat is a single, non-persisted conversation, so a
// fixed task id is all the agent needs to scope its checkpoints and working
// memory (§5.7.10 / decision 2026-06-23).
const DefaultTaskID = "chat"

// defaultSystemPrompt frames the standing pseudo-task: open-ended assistance with
// no completion criterion. Because the task can never be "done" or "failed",
// task_report/task_revert do not apply in chat — they are rejected by the
// emitter (see emitter.go). This is the chat role card, layered at L1 AFTER the
// kernel principles (§5.7.11) which the brain seeds itself.
const defaultSystemPrompt = `You are Plexus, assisting a user in an open-ended chat session.

Your standing task is simply to participate in the discussion and help with the
user's needs. This task has no fixed completion criterion: it is never "done" and
never "failed", so do not try to report or revert it.

Be direct and concise. Use your tools to inspect and act on the user's workspace
when it helps; delegate self-contained, noisy sub-work; keep your own context clean.`

// DefaultRoleCard returns the chat agent's role card.
func DefaultRoleCard() brain.RoleCard {
	return brain.RoleCard{SystemPrompt: defaultSystemPrompt}
}
