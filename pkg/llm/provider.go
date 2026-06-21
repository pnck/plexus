package llm

import (
	"context"
	"net/http"
)

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

// ModelLister is an optional capability a Provider may implement to enumerate
// the model IDs available to its configured account. Callers should type-assert
// a Provider to ModelLister and degrade gracefully when it is not implemented.
type ModelLister interface {
	// ListModels returns the available model IDs (sorted).
	ListModels(ctx context.Context) ([]string, error)
}

// HTTPMiddleware matches the Stainless-generated SDKs' option.Middleware alias,
// letting callers observe or modify raw HTTP traffic (used by chat's /debug mode).
type HTTPMiddleware = func(*http.Request, func(*http.Request) (*http.Response, error)) (*http.Response, error)
