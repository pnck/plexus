package chat

import (
	"context"
	"fmt"
	"sync"

	"plexus/pkg/llm"
)

// mutableGateway is the interactive, runtime-reconfigurable gateway `plexus chat`
// uses: it can start unconfigured (no key) so chat launches without one, and its
// provider/model/key/base-url can be changed live via the control channel without
// restarting the agent. It wraps the shared agent.GatewayConfig (aliased here as
// GatewayConfig) and its Build. The headless run daemon uses a static gateway
// instead. Safe for concurrent use.
type mutableGateway struct {
	mu  sync.RWMutex
	cfg GatewayConfig
	p   llm.Provider // nil until a valid config is built
}

// newMutableGateway builds from an initial config; a missing key is fine — the
// gateway is simply unconfigured until /key sets one.
func newMutableGateway(cfg GatewayConfig) *mutableGateway {
	g := &mutableGateway{cfg: cfg}
	if p, err := cfg.Build(); err == nil {
		g.p = p
	}
	return g
}

// NewMutableGateway returns a runtime-reconfigurable gateway as an llm.Provider.
// The host recognizes its concrete type and exposes the /key, /provider, /model,
// /models, /debug, /status control commands over it.
func NewMutableGateway(cfg GatewayConfig) llm.Provider { return newMutableGateway(cfg) }

// GenerateStream delegates to the current provider, or errors if unconfigured.
func (g *mutableGateway) GenerateStream(ctx context.Context, msgs []llm.Message, tools []llm.ToolDefinition) (llm.EventStream, error) {
	g.mu.RLock()
	p := g.p
	g.mu.RUnlock()
	if p == nil {
		return nil, ErrGatewayUnconfigured
	}
	return p.GenerateStream(ctx, msgs, tools)
}

// ListModels delegates to the current provider if it supports listing.
func (g *mutableGateway) ListModels(ctx context.Context) ([]string, error) {
	g.mu.RLock()
	p := g.p
	g.mu.RUnlock()
	if p == nil {
		return nil, ErrGatewayUnconfigured
	}
	lister, ok := p.(llm.ModelLister)
	if !ok {
		return nil, fmt.Errorf("provider %q does not support listing models", g.cfg.Provider)
	}
	return lister.ListModels(ctx)
}

// reconfigure applies mutate to a copy of the config and rebuilds the provider. On
// build failure the config is updated but the provider is cleared (so the next turn
// reports unconfigured) and the error is returned.
func (g *mutableGateway) reconfigure(mutate func(*GatewayConfig)) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	mutate(&g.cfg)
	p, err := g.cfg.Build()
	g.p = p // nil on error
	return err
}

// setRawObs installs the raw-observability sink and rebuilds so the raw-LLM
// middleware takes effect. A build error (e.g. no key yet) is ignored — the sink
// persists in the config and applies once the gateway becomes usable.
func (g *mutableGateway) setRawObs(sink func([]byte)) {
	_ = g.reconfigure(func(c *GatewayConfig) { c.RawObs = sink })
}

// status returns a snapshot of the current config and whether it is usable.
func (g *mutableGateway) status() (GatewayConfig, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cfg, g.p != nil
}
