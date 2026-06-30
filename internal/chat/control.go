package chat

import (
	"context"
	"fmt"
	"strings"

	"plexus/pkg/brain"
	"plexus/pkg/llm"
)

// Control commands (the /-commands' host side). Read-only commands run on the
// push goroutine via runCtrl; the two that mutate brain history (reset, system)
// run on the worker via runWorkerCtrl so they serialize with turns.

// isWorkerCtrl reports whether a control command must run on the worker goroutine
// (because it mutates the brain's history).
func isWorkerCtrl(cmd string) bool {
	return cmd == cmdReset || cmd == cmdSystem
}

// runWorkerCtrl handles the history-mutating commands. Called only on the worker.
func (h *Host) runWorkerCtrl(cmd, arg string) string {
	switch cmd {
	case cmdReset:
		h.agent.Brain.Reset()
		return "history cleared"
	case cmdSystem:
		if arg == "" { // no-arg = get (read-only; does not reset history)
			if rc := h.agent.Brain.RoleCard(); !rc.IsZero() {
				return rc.RenderSystemPrompt()
			}
			return "(no system prompt set)"
		}
		// A raw `/system <text>` is a freeform override: it goes in Guidance, which
		// RenderSystemPrompt renders verbatim when it is the only field set.
		h.agent.Brain.SetRoleCard(brain.RoleCard{Guidance: arg})
		return "system prompt set; history reset"
	default:
		return fmt.Sprintf("unknown control command %q", cmd)
	}
}

// runCtrl handles the read-only / gateway control commands.
func (h *Host) runCtrl(ctx context.Context, cmd, arg string) string {
	switch cmd {
	case cmdKey:
		if arg == "" { // no-arg = get (never clear the key by accident)
			return h.gwGet(func(c GatewayConfig) string {
				if c.APIKey != "" {
					return "api key is set"
				}
				return "no api key set — set one with /key <value>"
			})
		}
		return h.reconfigure("api key set", func(c *GatewayConfig) { c.APIKey = arg })
	case cmdProvider:
		if arg == "" { // no-arg = get
			return h.gwGet(func(c GatewayConfig) string { return "provider = " + c.Provider })
		}
		if arg != "openai" && arg != "anthropic" {
			return "usage: /provider openai|anthropic"
		}
		return h.reconfigure("provider set to "+arg, func(c *GatewayConfig) { c.Provider = arg })
	case cmdModel:
		if arg == "" { // no-arg = get
			return h.gwGet(func(c GatewayConfig) string { return "model = " + c.Model })
		}
		return h.reconfigure("model set to "+arg, func(c *GatewayConfig) { c.Model = arg })
	case cmdDebug:
		switch arg {
		case "": // no-arg = get
			return h.gwGet(func(c GatewayConfig) string {
				if c.Debug {
					return "debug = on"
				}
				return "debug = off"
			})
		case "on":
			return h.reconfigure("debug on", func(c *GatewayConfig) { c.Debug = true })
		case "off":
			return h.reconfigure("debug off", func(c *GatewayConfig) { c.Debug = false })
		default:
			return "usage: /debug on|off"
		}
	case cmdReasoning:
		level := strings.ToLower(arg)
		switch {
		case level == "": // no-arg = get (does NOT turn reasoning off)
			return h.gwGet(func(c GatewayConfig) string {
				if c.Reasoning == "" {
					return "reasoning = off"
				}
				return "reasoning = " + c.Reasoning
			})
		case level == "off" || level == "none":
			return h.reconfigure("reasoning off", func(c *GatewayConfig) { c.Reasoning = "" })
		case llm.ValidEffort(level):
			return h.reconfigure("reasoning effort = "+level, func(c *GatewayConfig) { c.Reasoning = level })
		default:
			return "usage: /reasoning " + strings.Join(llm.ReasoningEfforts, "|") + "|off"
		}
	case cmdModels:
		return h.listModels(ctx)
	case cmdStatus:
		return h.status()
	case cmdTools:
		return h.listTools()
	case cmdSteps:
		return h.listSteps(ctx)
	case cmdMemory:
		return h.listMemory(ctx)
	default:
		return fmt.Sprintf("unknown control command %q", cmd)
	}
}

// gwGet reads the current gateway config for a no-arg get command (read-only),
// or reports that the gateway is fixed.
func (h *Host) gwGet(show func(GatewayConfig) string) string {
	if h.gw == nil {
		return "gateway is not runtime-configurable"
	}
	cfg, _ := h.gw.status()
	return show(cfg)
}

// reconfigure mutates the gateway config and reports success or the build error.
func (h *Host) reconfigure(okMsg string, mutate func(*GatewayConfig)) string {
	if h.gw == nil {
		return "gateway is not runtime-configurable"
	}
	if err := h.gw.reconfigure(mutate); err != nil {
		return fmt.Sprintf("%v", err)
	}
	return okMsg
}

func (h *Host) listModels(ctx context.Context) string {
	if h.gw == nil {
		return "gateway is not runtime-configurable"
	}
	ids, err := h.gw.ListModels(ctx)
	if err != nil {
		return fmt.Sprintf("%v", err)
	}
	if len(ids) == 0 {
		return "(no models)"
	}
	return strings.Join(ids, "\n") + fmt.Sprintf("\n[%d models]", len(ids))
}

func (h *Host) status() string {
	if h.gw == nil {
		return "gateway: fixed (not runtime-configurable)"
	}
	cfg, ready := h.gw.status()
	key := "MISSING"
	if cfg.APIKey != "" {
		key = "set"
	}
	state := "ready"
	if !ready {
		state = "UNCONFIGURED"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "provider=%s model=%s key=%s state=%s", cfg.Provider, cfg.Model, key, state)
	if cfg.BaseURL != "" {
		fmt.Fprintf(&b, " base-url=%s", normalizeBaseURL(cfg.Provider, cfg.BaseURL))
	}
	if cfg.Reasoning != "" {
		fmt.Fprintf(&b, " reasoning=%s", cfg.Reasoning)
	}
	if cfg.Debug {
		fmt.Fprint(&b, " debug=on")
	}
	return b.String()
}

func (h *Host) listTools() string {
	effs := h.agent.Registry.List()
	if len(effs) == 0 {
		return "(no tools)"
	}
	var b strings.Builder
	for _, e := range effs {
		gate := ""
		if h.agent.Registry.RequiresApproval(e.Name()) {
			gate = " [approval]"
		}
		fmt.Fprintf(&b, "%-14s %-13s %s%s\n", e.Name(), e.Effects(), e.Description(), gate)
	}
	fmt.Fprintf(&b, "[%d tools]", len(effs))
	return b.String()
}

func (h *Host) listSteps(ctx context.Context) string {
	steps, err := h.agent.Checkpoints.Steps(ctx, DefaultTaskID)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if len(steps) == 0 {
		return "(no steps)"
	}
	var b strings.Builder
	for _, s := range steps {
		fmt.Fprintf(&b, "#%d [%s] %s", s.Seq, s.Status, s.Goal)
		if s.Result != "" {
			fmt.Fprintf(&b, " — %s", s.Result)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (h *Host) listMemory(ctx context.Context) string {
	notes, err := h.agent.WorkingMemory.List(ctx, DefaultTaskID)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if len(notes) == 0 {
		return "(working memory empty)"
	}
	var b strings.Builder
	for _, n := range notes {
		fmt.Fprintf(&b, "%s: %s\n", n.Key, n.Content)
	}
	return strings.TrimRight(b.String(), "\n")
}
