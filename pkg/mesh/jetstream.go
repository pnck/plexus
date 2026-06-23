package mesh

import (
	"context"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// JetStream topology for the Plexus 受治边 (governed edge). Only the durable work
// channels live on JetStream; observability (sys.obs.*), heartbeat and presence
// (sys.register) stay on core NATS by design — they are live/ephemeral signals,
// not persisted work (E1.2 boundary decision).
//
// These helpers are the single source of truth for stream config so the node
// (consumer) and the control-plane server (publisher) provision identical streams
// — CreateOrUpdateStream is idempotent, so whoever connects first wins and the
// other no-ops.
const (
	// StreamAgentWork captures every per-agent inbox (agent.<id>.inbox). Each
	// agent binds a durable consumer filtered to its own inbox, so a message
	// published while the agent is down is retained and replayed on reconnect.
	StreamAgentWork = "AGENT_WORK"

	// dedupWindow is the JetStream duplicate-detection window. Publishing with
	// Nats-Msg-Id = Message.ID makes delivery idempotent within this window
	// (at-least-once + dedup), per §4.1 / E1.1 (ID doubles as the dedup key).
	dedupWindow = 2 * time.Minute
)

// agentWorkSubjects is the wildcard the work stream binds: agent.*.inbox. It is
// derived from the configured inbox prefix so a non-default prefix still works.
func agentWorkSubjects(inboxPrefix string) []string {
	return []string{inboxPrefix + "*.inbox"}
}

// EnsureAgentWorkStream idempotently provisions the per-agent work stream backed
// by file storage with WorkQueue retention: each per-agent inbox subject is
// consumed by exactly one durable consumer, so an acked message is pruned (the
// inbox is a queue, not a log). Duplicate detection over dedupWindow honours
// Nats-Msg-Id for idempotent at-least-once delivery.
func EnsureAgentWorkStream(ctx context.Context, js jetstream.JetStream, inboxPrefix string) (jetstream.Stream, error) {
	return js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        StreamAgentWork,
		Subjects:    agentWorkSubjects(inboxPrefix),
		Storage:     jetstream.FileStorage,
		Retention:   jetstream.WorkQueuePolicy,
		Duplicates:  dedupWindow,
		Description: "Per-agent durable inbox (受治边 work delivery).",
	})
}

// inboxConsumerName is the durable consumer name for an agent's inbox. One
// durable per agent id keeps replay position across reconnects/restarts.
func inboxConsumerName(agentID string) string {
	return "inbox-" + agentID
}
