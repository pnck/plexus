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
	// —— Existing (preserved) ——
	ID        string      `json:"id"`        // Unique message identifier
	Sender    string      `json:"sender"`    // Agent ID or System component
	Target    string      `json:"target"`    // Target Agent ID or Group Name
	Type      MessageType `json:"type"`      // The type of the message
	Payload   []byte      `json:"payload"`   // The actual data
	Timestamp int64       `json:"timestamp"` // Unix timestamp

	// —— Dedup / correlation (JetStream, §5.6/§5.7.3) ——
	MessageID     string `json:"message_id"`     // = JetStream Nats-Msg-Id; stable idempotency key for at-least-once dedup
	CorrelationID string `json:"correlation_id"` // ask↔answer correlation (yield/resume, §5.7.5)
	ReplyTo       string `json:"reply_to"`       // Reply address — placeholder, semantics not locked (pending D2)

	// —— Authority layering (brain enforces layered rendering, §5.7.3) ——
	Authority  Authority `json:"authority"`  // L1..L5
	Provenance string    `json:"provenance"` // Source marker (role card / user / tool / control plane / memory)

	// —— Addressing anchors / audit (§5.7.3, accountability loop §4.5) ——
	NodeID    string `json:"node_id"`    // DAG node anchor (§4.2) — the agent's task-view unit
	SessionID string `json:"session_id"` // Session identifier
	TraceID   string `json:"trace_id"`   // Audit trace identifier
	// NOTE: no ProjectID. "project" is not a transport concept (agents only have a
	// DAG task view; multi-project is a control-plane/domain concern in E5). Decision 2026-06-21 / D1.
}
