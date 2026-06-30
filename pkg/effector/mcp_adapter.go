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

// mcpToolSource is what RegisterMCPClient needs: enumerating tools plus the
// per-tool call surface. Narrowing to an interface (mirroring mcpCaller) keeps
// the registration path testable with a fake — no live MCP server. The concrete
// *mcp.Client satisfies it.
type mcpToolSource interface {
	ListTools(ctx context.Context) ([]mcp.ToolInfo, error)
	mcpCaller
}

// mcpEffector adapts a single MCP tool (an mcp.ToolInfo plus the owning client)
// into an Effector. MCP tools do not declare effects, so the effect set is
// assigned at registration time (mapping onto our closed vocabulary).
type mcpEffector struct {
	info    mcp.ToolInfo
	effects EffectSet
	client  mcpCaller
}

func (m *mcpEffector) Name() string            { return m.info.Name }
func (m *mcpEffector) Description() string     { return m.info.Description }
func (m *mcpEffector) Effects() EffectSet      { return m.effects }
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

// EffectMap maps an MCP tool name to its EffectSet. MCP tools do not declare
// effects, so the operator supplies them here at registration (mapping onto our
// closed vocabulary). Tools absent from the map default to exec.arbitrary — the
// unbounded escape hatch — for safety (an undeclared/untrusted MCP effect is
// gated like a generic shell; bwrap remains its hard floor).
type EffectMap map[string]EffectSet

// EffectsFor returns the configured EffectSet for a tool name, defaulting to
// {exec.arbitrary} when the tool is unknown (fail-safe to the gated escape hatch).
func (m EffectMap) EffectsFor(name string) EffectSet {
	if e, ok := m[name]; ok {
		return e
	}
	return NewEffectSet(ExecArbitrary)
}

// AdaptTool wraps a single MCP tool as an Effector, assigning its EffectSet from
// the supplied EffectMap (unknown tools default to {exec.arbitrary}). client is
// the MCP call surface (a *mcp.Client in production).
func AdaptTool(info mcp.ToolInfo, client mcpCaller, effects EffectMap) Effector {
	return &mcpEffector{
		info:    info,
		effects: effects.EffectsFor(info.Name),
		client:  client,
	}
}

// RegisterMCPClient lists the tools advertised by client and registers each as
// an Effector in r, assigning effect sets from effects (unknown -> {exec.arbitrary}).
// It returns the effectors registered. client is an MCP tool source (a *mcp.Client
// in production).
func RegisterMCPClient(ctx context.Context, r *Registry, client mcpToolSource, effects EffectMap) ([]Effector, error) {
	tools, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Effector, 0, len(tools))
	for _, info := range tools {
		e := AdaptTool(info, client, effects)
		r.Register(e)
		out = append(out, e)
	}
	return out, nil
}
