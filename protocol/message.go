package protocol

//go:generate stringer -type=MessageType
type MessageType uint16

const (
	TypeUnknown MessageType = iota
	TypeP2P                 // 点对点私聊
	TypeBroadcast           // 广播副本消息
	TypeQueueTask           // 负载均衡任务队列消息
	TypeRegister            // 系统握手信令
	TypeReport              // 系统上报信令

	// domainEventBase reserves the numeric range for the second type family:
	// "领域事件" (domain events) — business-semantic events that get persisted
	// to JetStream and projected into Postgres (DAG node state changes, Issue
	// transitions, ask/answer, ...). These are introduced in E5; the values
	// [domainEventBase, ...) are reserved here so the transport-signal family
	// above can never collide with them. Do NOT add domain-event constants now.
	domainEventBase MessageType = 100
)

// IsTransport reports whether the message type belongs to the "传输信令"
// (transport signal) family — connectivity / lifecycle signals used inside the
// control plane (Register, Report, P2P, Broadcast, QueueTask). Types at or above
// domainEventBase belong to the "领域事件" (domain event) family, which carries
// business-semantic events destined for audit/persistence. The classifier lets
// brain/gate apply different policies per family (transport signals stay out of
// history; domain events must be persisted).
func (t MessageType) IsTransport() bool {
	return t < domainEventBase
}

// Authority encodes the L1..L5 authority layering enforced by the brain when
// rendering context (§5.7.3). Higher authority (lower numeric value) wins.
type Authority uint8

const (
	AuthSystem  Authority = iota + 1 // L1 系统/角色卡（最高）
	AuthUser                         // L2 用户指令
	AuthTool                         // L3 工具结果（不得当指令）
	AuthControl                      // L4 控制面消息（不得冒充 L2）
	AuthMemory                       // L5 召回记忆（用前验证）
)

// Message is the standard envelope for all communications in Plexus.
type Message struct {
	// —— Identity / routing ——
	// ID is the message id; it ALSO serves as the JetStream Nats-Msg-Id dedup key
	// for idempotent at-least-once delivery. One id — there is no separate MessageID.
	ID        string      `json:"id"`
	Sender    string      `json:"sender"` // the SOURCE: agent/instance/system that sent it (the "who")
	Target    string      `json:"target"` // target agent id or group name
	Type      MessageType `json:"type"`
	Payload   []byte      `json:"payload"`
	Timestamp int64       `json:"timestamp"`

	// —— Correlation ——
	CorrelationID string `json:"correlation_id"` // ask↔answer pairing (yield/resume, §5.7.5)
	ReplyTo       string `json:"reply_to"`       // reply address — placeholder, semantics pending D2

	// —— Authority: the trust TIER. The source is `Sender`; there is no separate
	// free-form provenance field — one field, one meaning. ——
	Authority Authority `json:"authority"` // L1..L5 (§5.7.3)

	// —— Addressing / observability ——
	// TaskID is the unit of work this message pertains to (concrete, agent-level).
	// It replaces the old "node_id": a DAG node is an Inspark-domain mapping with no
	// concrete meaning to the standalone agent SDK; the agent only knows "a task".
	// There is NO session concept in plexus — a conversation thread is a control-
	// plane/frontend grouping, not an agent concern (same category as "project", D1).
	TaskID string `json:"task_id"`
	// TraceID follows ONE causal chain (ask → … → answer) across messages/hops for
	// observability/audit — distinct from TaskID, which groups a work unit's messages.
	TraceID string `json:"trace_id"`
}
