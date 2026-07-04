package chat

import "plexus/pkg/agent"

// The core agent assembly + gateway resolution live in pkg/agent (tag-free, no
// readline), so the headless `run` daemon can reuse them without dragging in the
// chat REPL. chat aliases them here so its host / control / REPL code keeps its
// familiar names, and layers the interactive live-reconfigurable gateway
// (mutableGateway, gateway.go) and the Host (host.go) on top.
type (
	Config        = agent.Config
	Agent         = agent.Agent
	GatewayConfig = agent.GatewayConfig
	rejectEmitter = agent.RejectEmitter
)

var (
	New                    = agent.New
	ResolveGateway         = agent.ResolveGateway
	ErrGatewayUnconfigured = agent.ErrGatewayUnconfigured
)
