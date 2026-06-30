// Package chat is the cohesive home of the `plexus chat` product: a fully
// assembled agent (brain + effector + delegation + memory + checkpoint) and the
// chat-specific pieces that make it a runnable conversational agent — the
// default role card and standing task, the task-channel reject policy, and (in
// later E2.6 nodes) the CS host, interactive approver and REPL client. The
// generic framework stays in pkg/*; only the chat-specific defaults live here.
package chat

import (
	_ "embed"
	"fmt"

	"plexus/pkg/brain"
)

// DefaultTaskID is the standing pseudo-task every chat turn is scoped to. plexus
// has no session concept; a chat is a single, non-persisted conversation, so a
// fixed task id is all the agent needs to scope its checkpoints and working
// memory (§5.7.10 / decision 2026-06-23).
const DefaultTaskID = "chat"

// chatRoleCardYAML is the chat role card as a real on-disk YAML, embedded so chat
// goes through the SAME parse path (brain.ParseRoleCard) a cluster instance uses
// for brain.LoadRoleCard (E3.3 dogfood). It frames the standing pseudo-task:
// open-ended assistance with no completion criterion, so task_report/task_revert
// do not apply (they are rejected by the emitter, see emitter.go).
//
//go:embed rolecards/chat.yaml
var chatRoleCardYAML []byte

// DefaultRoleCard returns the chat agent's role card, parsed from the embedded
// YAML. A parse failure is a programmer error (the asset ships with the binary),
// so it panics rather than returning an error.
func DefaultRoleCard() brain.RoleCard {
	rc, err := brain.ParseRoleCard(chatRoleCardYAML, "embedded chat.yaml")
	if err != nil {
		panic(fmt.Sprintf("chat: embedded role card invalid: %v", err))
	}
	return rc
}
