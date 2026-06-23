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

	// Reasoning carries provider-opaque thinking blocks that must be replayed
	// verbatim on an assistant turn that also called tools (Anthropic extended
	// thinking requires the signed thinking block to precede the tool_use it
	// led to). It is NOT human-facing context and providers that don't need it
	// (OpenAI) ignore it.
	Reasoning []ReasoningBlock
}

// ReasoningBlock is one completed thinking block emitted by the model. Text is
// the reasoning; Signature is the provider's opaque attestation required to
// replay it (empty when the provider does not sign thinking).
type ReasoningBlock struct {
	Text      string
	Signature string
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
	// DeltaText is the newly generated text chunk (the answer).
	DeltaText string

	// DeltaThinking is a newly generated chunk of the model's reasoning/thinking
	// (Anthropic thinking blocks, OpenAI-compatible reasoning_content, or text
	// inside <think>…</think>). It is shown to the user but is NOT part of the
	// answer and does not re-enter history.
	DeltaThinking string

	// ToolCall is populated if the model is invoking a tool.
	ToolCall *ToolCall

	// ReasoningBlock is populated on the event that closes a thinking block,
	// carrying the full reasoning text plus its signature for verbatim replay.
	// (DeltaThinking carries the same text incrementally for live display; this
	// is the once-per-block, signed form used to round-trip through history.)
	ReasoningBlock *ReasoningBlock

	// FinishReason indicates why generation stopped (e.g., "stop", "tool_calls", "length").
	FinishReason string

	// Usage is populated on the terminal event when the provider reports token usage.
	Usage *Usage

	// Error captures any error that occurred during streaming.
	Error error
}
