// Package mcp provides a clean, SDK-agnostic client for the Model Context
// Protocol (MCP). It wraps a maintained Go MCP SDK underneath so that the rest
// of plexus is not coupled to that SDK's surface: callers depend only on the
// small API defined here (Connect / ListTools / CallTool plus the ToolInfo and
// ToolResult value types).
//
// The MCP client is owned by the agent/brain. Delegations never hold an MCP
// client directly (§5.7.4); they reach tools only through the mediated
// capability handle in pkg/effector.
package mcp

import "encoding/json"

// ToolInfo describes a single tool exposed by an MCP server. It is the
// SDK-agnostic projection of the protocol's tool descriptor: enough to surface
// the tool to an LLM (name + description + input JSON Schema) without leaking
// the underlying SDK types.
type ToolInfo struct {
	// Name is the unique tool identifier used when calling the tool.
	Name string
	// Description is a human-readable hint for the model.
	Description string
	// InputSchema is the JSON Schema object describing the tool's arguments.
	// It is the raw schema as reported by the server.
	InputSchema json.RawMessage
}

// ToolResult is the SDK-agnostic projection of a tool invocation result.
type ToolResult struct {
	// Content is the flattened textual content of the tool result. Non-text
	// content blocks are rendered to a best-effort textual placeholder.
	Content string
	// IsError reports whether the tool reported a tool-level error. This is a
	// tool-level (not transport-level) error and is meant to be fed back to the
	// LLM for self-correction, per the MCP spec.
	IsError bool
}
