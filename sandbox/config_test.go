package sandbox

import "testing"

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
