package llm

import "context"

// EventStream defines a synchronous iterator for stream events.
type EventStream interface {
	// Next advances the stream to the next event. Returns false when the stream ends or an error occurs.
	Next() bool
	// Current returns the current event.
	Current() StreamEvent
	// Err returns the first error that was encountered by the stream.
	Err() error
	// Close releases any resources associated with the stream.
	Close() error
}

// Provider is the unified interface for interacting with various LLM backends.
type Provider interface {
	// GenerateStream takes a list of historical messages and available tools,
	// and returns a synchronous iterator that yields generation events.
	GenerateStream(ctx context.Context, msgs []Message, tools []ToolDefinition) (EventStream, error)
}
