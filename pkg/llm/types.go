package llm

// MessageRole defines the role of the message sender.
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// Message is a unified representation of a chat message.
type Message struct {
	Role    MessageRole
	Content string

	// ToolCalls represents the tools that the assistant wants to call.
	ToolCalls []ToolCall

	// ToolCallID is used when Role == RoleTool to specify which call this result is for.
	ToolCallID string
}

// ToolCall represents a single tool invocation request from the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON string of arguments
}

// ToolDefinition is a unified representation of a tool/function schema.
type ToolDefinition struct {
	Name        string
	Description string
	// Parameters is a JSON schema object defining the tool's input structure.
	Parameters any
}

// Usage reports token consumption for a generation.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// StreamEvent represents a single event in the streaming response.
type StreamEvent struct {
	// DeltaText is the newly generated text chunk.
	DeltaText string

	// ToolCall is populated if the model is invoking a tool.
	ToolCall *ToolCall

	// FinishReason indicates why generation stopped (e.g., "stop", "tool_calls", "length").
	FinishReason string

	// Usage is populated on the terminal event when the provider reports token usage.
	Usage *Usage

	// Error captures any error that occurred during streaming.
	Error error
}
