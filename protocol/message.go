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
)

// Message is the standard envelope for all communications in Plexus.
type Message struct {
	ID        string      `json:"id"`        // Unique message identifier
	Sender    string      `json:"sender"`    // Agent ID or System component
	Target    string      `json:"target"`    // Target Agent ID or Group Name
	Type      MessageType `json:"type"`      // The type of the message
	Payload   []byte      `json:"payload"`   // The actual data
	Timestamp int64       `json:"timestamp"` // Unix timestamp
}
