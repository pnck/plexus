// Package netpol models an agent's network sandbox policy (E4.5): the per-role
// egress rules declared in the role card's `network` section, and the pure
// functions that lower a policy into nftables / ip-rule text (nftgen.go).
//
// Model (docs/implement-design.md §5.6.7, tracking/E4-establishment-flow.md §6):
// default deny-all; each transport protocol (tcp/udp) gets an action in
// {redirect, reject, drop} with drop the default; a log scope is orthogonal to
// the action. Setup reads this in Phase 0 and generates the nft ruleset — allowed
// TCP/UDP is transparently intercepted via TPROXY to plexus's local egress port
// and relayed out through the control plane's EgressRelay.
//
// This is a leaf types package (stdlib + yaml only). The role card carries a
// *NetPolicy; the brain does not render or gate on it — Setup consumes it.
package netpol

import (
	"fmt"

	yaml "gopkg.in/yaml.v3"
)

// NetAction is what happens to a protocol's egress. The zero value is Drop, so an
// unspecified protocol is denied by default (deny-all baseline).
type NetAction int

const (
	Drop     NetAction = iota // silently dropped (default; falls through to nft policy drop)
	Reject                    // actively refused (TCP reset / ICMP)
	Redirect                  // allowed: transparently proxied to CP's EgressRelay
)

func (a NetAction) String() string {
	switch a {
	case Redirect:
		return "redirect"
	case Reject:
		return "reject"
	default:
		return "drop"
	}
}

func parseAction(s string) (NetAction, error) {
	switch s {
	case "", "drop":
		return Drop, nil
	case "reject":
		return Reject, nil
	case "redirect":
		return Redirect, nil
	default:
		return Drop, fmt.Errorf("netpol: invalid action %q (want redirect|reject|drop)", s)
	}
}

// UnmarshalYAML lets a NetPolicy field accept the role-card token directly.
func (a *NetAction) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := parseAction(s)
	if err != nil {
		return err
	}
	*a = v
	return nil
}

// LogScope selects which protocols' egress is audited. Zero value is Off.
type LogScope int

const (
	LogOff LogScope = iota
	LogAll
	LogTCPOnly
	LogUDPOnly
)

func (l LogScope) String() string {
	switch l {
	case LogAll:
		return "all"
	case LogTCPOnly:
		return "tcp_only"
	case LogUDPOnly:
		return "udp_only"
	default:
		return "off"
	}
}

func parseLog(s string) (LogScope, error) {
	switch s {
	case "", "off":
		return LogOff, nil
	case "all":
		return LogAll, nil
	case "tcp_only":
		return LogTCPOnly, nil
	case "udp_only":
		return LogUDPOnly, nil
	default:
		return LogOff, fmt.Errorf("netpol: invalid log %q (want all|off|tcp_only|udp_only)", s)
	}
}

func (l *LogScope) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := parseLog(s)
	if err != nil {
		return err
	}
	*l = v
	return nil
}

func (l LogScope) logs(proto Proto) bool {
	switch proto {
	case UDP:
		return l == LogAll || l == LogUDPOnly
	default:
		return l == LogAll || l == LogTCPOnly
	}
}

// Proto is a transport-protocol selector for Decide / generation.
type Proto int

const (
	TCP Proto = iota
	UDP
)

func (p Proto) String() string {
	if p == UDP {
		return "udp"
	}
	return "tcp"
}

// NetPolicy is the role card's `network` section: a per-protocol egress action
// plus a log scope. The zero value is fully denied and unaudited — the safe
// baseline for a role that declares no `network` at all.
type NetPolicy struct {
	TCP NetAction `yaml:"tcp"`
	UDP NetAction `yaml:"udp"`
	Log LogScope  `yaml:"log"`
}

// Decide returns the action for a protocol. v1 is per-protocol; the per-target /
// per-process refinement rides on top of this at the proxy + CP (E5), keyed off
// the flow attribution — it does not change this coarse decision.
func (p NetPolicy) Decide(proto Proto) NetAction {
	if proto == UDP {
		return p.UDP
	}
	return p.TCP
}

// logs reports whether egress on a protocol is audited.
func (p NetPolicy) logs(proto Proto) bool { return p.Log.logs(proto) }

// Parse decodes a startup network config (YAML) into a NetPolicy. Missing fields
// default to Drop (action) / Off (log) — i.e. deny-all. Invalid tokens are an
// error. NetPolicy is immutable launch config (injected at start, consumed by
// Setup); it is NOT a role-card field — the role card is soft LLM-facing guidance,
// while this is a hard sandbox switch the agent cannot change.
func Parse(data []byte) (NetPolicy, error) {
	var p NetPolicy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return NetPolicy{}, fmt.Errorf("netpol: %w", err)
	}
	return p, nil
}

// Describe renders the LLM-facing summary of the effective egress limits — the
// concrete "what can I reach" the agent's cognition needs. It feeds the sandbox
// environment-state L1 frame (which the brain injects after the kernel and role
// card): the LLM reasons about its current limits, never about the mechanism
// (tproxy/nft/CP proxy) that enforces them.
func (p NetPolicy) Describe() string {
	if p.TCP == p.UDP {
		return "Outbound network (TCP and UDP): " + describeAction(p.TCP) + "."
	}
	return "Outbound network — TCP: " + describeAction(p.TCP) + "; UDP: " + describeAction(p.UDP) + "."
}

func describeAction(a NetAction) string {
	switch a {
	case Redirect:
		return "allowed, routed through the control-plane egress proxy (all traffic is audited)"
	case Reject:
		return "refused"
	default: // Drop
		return "blocked"
	}
}
