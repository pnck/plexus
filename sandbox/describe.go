package sandbox

import (
	"strings"

	"plexus/sandbox/bwrap"
	"plexus/sandbox/netpol"
)

// Environment is the startup-determined sandbox state — everything decided before
// the agent runs and immutable thereafter: filesystem confinement, network egress
// policy, and resource limits. All of it is fixed by the launcher; the agent cannot
// change any of it.
//
// Describe() renders it all into one LLM-facing block for the sandbox
// environment-state L1 frame (the brain injects it after the kernel and role
// card), so the agent knows its concrete constraints up front instead of
// discovering them by trial. The mechanism (bwrap flags / nft / tproxy) is never
// surfaced — only the resulting limits the agent must reason about (E4.5).
type Environment struct {
	Policy bwrap.Policy // filesystem + namespace confinement

	// Net is the egress fence (cluster mode). A nil Net means the network is
	// UNMANAGED — host network, no per-agent restrictions — which is the single-
	// process chat/dev case: there is no CP EgressRelay to route through, so no
	// fence applies.
	Net *netpol.NetPolicy

	Limits Rlimits // resource ceilings (E4.3); the zero value describes no limits
}

// Describe composes each startup-fixed face into the environment-state frame. A new
// sandbox face plugs in by contributing its own Describe here.
func (e Environment) Describe() string {
	parts := []string{"Your sandbox environment (fixed at startup; you cannot change it):"}
	if fs := e.Policy.Describe(); fs != "" {
		parts = append(parts, fs)
	}
	if e.Net != nil {
		parts = append(parts, e.Net.Describe())
	} else {
		parts = append(parts, "Outbound network: host network, no per-agent restrictions.")
	}
	if lim := e.Limits.Describe(); lim != "" {
		parts = append(parts, lim)
	}
	return strings.Join(parts, "\n")
}
