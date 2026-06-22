package effector

import (
	"context"
	"encoding/json"

	"plexus/pkg/mcp"
)

// mcpCaller is the subset of *mcp.Client the adapter needs. Narrowing to an
// interface keeps the adapter testable with a fake client (no live MCP server).
type mcpCaller interface {
	CallTool(ctx context.Context, name string, args json.RawMessage) (mcp.ToolResult, error)
}

// mcpEffector adapts a single MCP tool (an mcp.ToolInfo plus the owning client)
// into an Effector. MCP tools do not carry a risk tag, so the tag is assigned at
// registration time.
type mcpEffector struct {
	info   mcp.ToolInfo
	risk   RiskTag
	client mcpCaller
}

func (m *mcpEffector) Name() string            { return m.info.Name }
func (m *mcpEffector) Description() string     { return m.info.Description }
func (m *mcpEffector) Risk() RiskTag           { return m.risk }
func (m *mcpEffector) Schema() json.RawMessage { return m.info.InputSchema }

// Invoke forwards the call to the MCP client. A tool-level MCP error is surfaced
// via Result.IsError (for LLM self-correction); a transport error is returned as
// a Go error.
func (m *mcpEffector) Invoke(ctx context.Context, args json.RawMessage) (Result, error) {
	res, err := m.client.CallTool(ctx, m.info.Name, args)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: res.Content, IsError: res.IsError}, nil
}

// RiskMap maps an MCP tool name to its RiskTag. MCP tools do not advertise a
// risk tier, so the operator supplies one here at registration. Tools absent
// from the map default to ExecArbitrary — the highest tier — for safety.
type RiskMap map[string]RiskTag

// RiskFor returns the configured RiskTag for a tool name, defaulting to
// ExecArbitrary when the tool is unknown (fail-safe to the highest tier).
func (m RiskMap) RiskFor(name string) RiskTag {
	if r, ok := m[name]; ok {
		return r
	}
	return ExecArbitrary
}

// AdaptTool wraps a single MCP tool as an Effector, assigning its RiskTag from
// the supplied RiskMap (unknown tools default to ExecArbitrary).
func AdaptTool(info mcp.ToolInfo, client *mcp.Client, risks RiskMap) Effector {
	return &mcpEffector{
		info:   info,
		risk:   risks.RiskFor(info.Name),
		client: client,
	}
}

// RegisterMCPClient lists the tools advertised by client and registers each as
// an Effector in r, assigning risk tags from risks (unknown -> ExecArbitrary). It returns
// the effectors registered.
func RegisterMCPClient(ctx context.Context, r *Registry, client *mcp.Client, risks RiskMap) ([]Effector, error) {
	tools, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Effector, 0, len(tools))
	for _, info := range tools {
		e := AdaptTool(info, client, risks)
		r.Register(e)
		out = append(out, e)
	}
	return out, nil
}
