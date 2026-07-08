package fence

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"plexus/sandbox/netpol"
)

// recorder is the fake Builder: it records the orchestration calls (so the SEQUENCE is
// asserted without any privilege) and can inject a failure at a named step to check
// fail-fast.
type recorder struct {
	calls       []string
	fencePolicy netpol.NetPolicy
	fenceParams netpol.Params
	failAt      string // method-name prefix to fail at ("" = never)
}

func (r *recorder) log(s string) error {
	r.calls = append(r.calls, s)
	if r.failAt != "" && strings.HasPrefix(s, r.failAt) {
		return fmt.Errorf("injected failure at %s", r.failAt)
	}
	return nil
}

func (r *recorder) UpLoopback() error { return r.log("UpLoopback") }
func (r *recorder) SetupVeth(peer, cidr, gw string) error {
	return r.log("SetupVeth " + peer + " " + cidr + " " + gw)
}
func (r *recorder) ApplyEgressFence(pol netpol.NetPolicy, pr netpol.Params) error {
	r.fencePolicy, r.fenceParams = pol, pr
	return r.log("ApplyEgressFence")
}
func (r *recorder) LimitResources(agentID string, _ Limits) error {
	return r.log("LimitResources " + agentID)
}
func (r *recorder) SpawnAgent(_ int, agent Cmd) error {
	return r.log("SpawnAgent " + strings.Join(agent.Argv, ","))
}

func testPlan() Plan {
	return Plan{
		AgentID:   "agent-1",
		VethPeer:  "plxa0",
		AgentCIDR: "10.242.42.2/30",
		Gateway:   "10.242.42.1",
		Net:       netpol.NetPolicy{TCP: netpol.Redirect, UDP: netpol.Drop},
		NFT:       netpol.Params{CP: "10.242.42.1", BusPort: 4222, EgressPort: 1080, Mark: 0x1, Table: 100, MaxConns: 64},
		Limits:    Limits{MemoryMax: 512 << 20, PidsMax: 128},
		Agent:     Cmd{Argv: []string{"plexus", "run", "--id", "agent-1", "--sandbox"}},
	}
}

// Build wires the sequence exactly, inside the userns-owned netns: lo up, then the nft
// fence + tproxy reroute, then the cgroup, then open sockets + exec.
func TestBuildSequence(t *testing.T) {
	r := &recorder{}
	if err := Build(testPlan(), r); err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{
		"UpLoopback",
		"SetupVeth plxa0 10.242.42.2/30 10.242.42.1",
		"ApplyEgressFence",
		"LimitResources agent-1",
		"SpawnAgent plexus,run,--id,agent-1,--sandbox",
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("call sequence:\n got  %v\n want %v", r.calls, want)
	}
	// ApplyEgressFence received the plan's policy + params; the fence they render (the
	// golden GenerateNFT the real builder mirrors) is the real deny-all fence.
	if r.fenceParams != testPlan().NFT {
		t.Fatalf("fence params = %+v", r.fenceParams)
	}
	nft, err := netpol.GenerateNFT(r.fencePolicy, r.fenceParams)
	if err != nil {
		t.Fatalf("render fence: %v", err)
	}
	for _, sub := range []string{
		"policy drop",
		"ip daddr 10.242.42.1 tcp dport 4222 accept",
		"meta l4proto tcp",
		"meta mark set 0x1",
	} {
		if !strings.Contains(nft, sub) {
			t.Fatalf("fence missing %q:\n%s", sub, nft)
		}
	}
}

// Fail-closed: an invalid plan (CP not a bare IPv4 — the nft-injection vector) must fail
// during pure generation, before ANY kernel object is built.
func TestBuildFailClosed(t *testing.T) {
	p := testPlan()
	p.NFT.CP = "1.2.3.4 accept\n evil"
	r := &recorder{}
	if err := Build(p, r); err == nil {
		t.Fatal("Build must fail on an invalid CP")
	}
	if len(r.calls) != 0 {
		t.Fatalf("fail-closed: no kernel op before a valid fence, got %v", r.calls)
	}
}

// Fail-fast: a builder error aborts the sequence — the cgroup and the agent exec never
// run, so a half-built fence never launches an agent.
func TestBuildAbortsOnError(t *testing.T) {
	r := &recorder{failAt: "ApplyEgressFence"}
	if err := Build(testPlan(), r); err == nil {
		t.Fatal("Build must propagate the builder error")
	}
	var sawFence bool
	for _, c := range r.calls {
		if strings.HasPrefix(c, "LimitResources") || strings.HasPrefix(c, "SpawnAgent") {
			t.Fatalf("must abort at ApplyEgressFence, but ran %q", c)
		}
		if strings.HasPrefix(c, "ApplyEgressFence") {
			sawFence = true
		}
	}
	if !sawFence {
		t.Fatal("ApplyEgressFence should have run (and failed)")
	}
}

// A deny-all policy still gets a fence applied (the nft policy-drop); its reroute is
// empty, which is the builder's concern (GenerateIPRules returns nil for it).
func TestBuildDenyAllStillFences(t *testing.T) {
	p := testPlan()
	p.Net = netpol.NetPolicy{} // zero = tcp/udp drop
	r := &recorder{}
	if err := Build(p, r); err != nil {
		t.Fatalf("Build: %v", err)
	}
	nft, err := netpol.GenerateNFT(r.fencePolicy, r.fenceParams)
	if err != nil || !strings.Contains(nft, "policy drop") {
		t.Fatalf("deny-all fence must still be applied: err=%v\n%s", err, nft)
	}
	if rr, _ := netpol.GenerateIPRules(r.fencePolicy, r.fenceParams); rr != nil {
		t.Fatalf("deny-all needs no tproxy reroute, got %v", rr)
	}
}
