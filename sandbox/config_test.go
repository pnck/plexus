package sandbox

import (
	"fmt"
	"testing"
)

// netTuple is the comparable subset of the fence-relevant net fields (Config itself has
// slice fields and so isn't ==-comparable).
func netTuple(c Config) [6]string {
	return [6]string{c.VethHost, c.VethPeer, c.HostCIDR, c.AgentCIDR, c.Gateway, c.CP}
}

// DefaultConfig must populate every field the network fence relies on, so a Config built
// programmatically (not via addSandboxFlags) is a valid full sandbox and never reaches
// SetupVeth with empty veth/CIDR/gateway.
func TestDefaultConfigPopulatesFenceFields(t *testing.T) {
	d := DefaultConfig()
	for name, got := range map[string]string{
		"VethHost":  d.VethHost,
		"VethPeer":  d.VethPeer,
		"HostCIDR":  d.HostCIDR,
		"AgentCIDR": d.AgentCIDR,
		"Gateway":   d.Gateway,
		"CP":        d.CP,
		"NetTCP":    d.NetTCP,
		"NetUDP":    d.NetUDP,
	} {
		if got == "" {
			t.Errorf("DefaultConfig().%s is empty — the fence would fail to build", name)
		}
	}
	if d.BusPort == 0 || d.EgressPort == 0 || d.Table == 0 || d.Mark == 0 {
		t.Errorf("DefaultConfig() left a required numeric knob at zero: %+v", d)
	}
	// Deny-all by default: no flag switches a feature off, so the baseline is closed.
	if d.NetTCP != "drop" || d.NetUDP != "drop" {
		t.Errorf("DefaultConfig() must default to deny-all egress, got tcp=%q udp=%q", d.NetTCP, d.NetUDP)
	}
}

// The per-agent net derivation must be deterministic in AgentID (the launcher and the
// re-exec'd fence stage compute it independently and must agree) and stay within IFNAMSIZ.
func TestDeriveAgentNetIsDeterministic(t *testing.T) {
	mk := func(id string) Config {
		c := DefaultConfig()
		c.AgentID = id
		c.deriveAgentNet()
		return c
	}
	a := mk("agent-1")
	if b := mk("agent-1"); netTuple(a) != netTuple(b) {
		t.Fatalf("derivation not deterministic:\n %v\n %v", netTuple(a), netTuple(b))
	}
	if len(a.VethHost) >= 16 || len(a.VethPeer) >= 16 {
		t.Fatalf("veth name exceeds IFNAMSIZ(16): %q/%q", a.VethHost, a.VethPeer)
	}
	if a.CP != a.Gateway {
		t.Fatalf("CP should track the derived gateway: CP=%q gw=%q", a.CP, a.Gateway)
	}
}

// Concurrent agents must not collide: across many AgentIDs the derived veth names spread
// out (a handful of birthday collisions in 16384 slots is fine; a near-flat map is not).
func TestDeriveAgentNetSpreadsAgents(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		c := DefaultConfig()
		c.AgentID = fmt.Sprintf("agent-%d", i)
		c.deriveAgentNet()
		seen[c.VethHost] = true
	}
	if len(seen) < 95 {
		t.Fatalf("per-agent veth spreading too weak: only %d distinct of 100", len(seen))
	}
}

// An empty AgentID leaves the base defaults untouched; an explicit net override is kept.
func TestDeriveAgentNetRespectsOverrides(t *testing.T) {
	c := DefaultConfig()
	c.deriveAgentNet() // AgentID == ""
	if netTuple(c) != netTuple(DefaultConfig()) {
		t.Fatalf("empty AgentID must leave defaults untouched, got %v", netTuple(c))
	}

	pinned := DefaultConfig()
	pinned.AgentID = "agent-1"
	pinned.VethHost = "myveth"
	pinned.deriveAgentNet()
	if pinned.VethHost != "myveth" || pinned.HostCIDR != DefaultConfig().HostCIDR {
		t.Fatalf("explicit override must be preserved, got host=%q cidr=%q", pinned.VethHost, pinned.HostCIDR)
	}
}
