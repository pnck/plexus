package setup

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"plexus/sandbox/netpol"
)

// recorder is the fake Executor: it records the orchestration calls (so the
// SEQUENCE is asserted without any privilege) and can inject a failure at a named
// step to check fail-fast.
type recorder struct {
	calls       []string
	fencePolicy netpol.NetPolicy
	fenceParams netpol.Params
	failAt      string // method-name prefix to fail at ("" = never)
}

func nsLabel(n string) string {
	if n == "" {
		return "host"
	}
	return n
}

func (r *recorder) log(s string) error {
	r.calls = append(r.calls, s)
	if r.failAt != "" && strings.HasPrefix(s, r.failAt) {
		return fmt.Errorf("injected failure at %s", r.failAt)
	}
	return nil
}

func (r *recorder) CreateNetns(n string) error       { return r.log("CreateNetns " + n) }
func (r *recorder) CreateVethPair(h, a string) error { return r.log("CreateVethPair " + h + " " + a) }
func (r *recorder) MoveToNetns(i, n string) error    { return r.log("MoveToNetns " + i + " " + n) }
func (r *recorder) SetAddr(n, i, c string) error {
	return r.log("SetAddr " + nsLabel(n) + " " + i + " " + c)
}
func (r *recorder) SetLinkUp(n, i string) error       { return r.log("SetLinkUp " + nsLabel(n) + " " + i) }
func (r *recorder) AddDefaultRoute(n, g string) error { return r.log("AddDefaultRoute " + n + " " + g) }
func (r *recorder) ApplyFence(n string, pol netpol.NetPolicy, pr netpol.Params) error {
	r.fencePolicy, r.fenceParams = pol, pr
	return r.log("ApplyFence " + n)
}
func (r *recorder) CreateCgroup(name string, _ CgroupLimits) error {
	return r.log("CreateCgroup " + name)
}
func (r *recorder) EnterAndExec(n, cg string, _ int, argv, _ []string) error {
	return r.log("EnterAndExec " + n + " " + cg + " " + strings.Join(argv, ","))
}

func testPlan() Plan {
	return Plan{
		AgentID:   "agent-1",
		Netns:     "ns-agent-1",
		VethHost:  "veth-h1",
		VethPeer:  "veth-a1",
		HostCIDR:  "10.0.0.1/30",
		AgentCIDR: "10.0.0.2/30",
		Gateway:   "10.0.0.1",
		Net:       netpol.NetPolicy{TCP: netpol.Redirect, UDP: netpol.Drop},
		NFT:       netpol.Params{CP: "10.0.0.1", BusPort: 4222, EgressPort: 1080, Mark: 0x1, Table: 100, MaxConns: 64},
		Cgroup:    CgroupLimits{MemoryMax: 512 << 20, PidsMax: 128},
		Argv:      []string{"plexus", "run", "--id", "agent-1", "--sandbox"},
	}
}

// Setup wires the flow-doc §2 sequence exactly: netns + veth (route only to CP),
// then the nft fence + tproxy reroute, then the cgroup, then enter+exec.
func TestSetupSequence(t *testing.T) {
	r := &recorder{}
	if err := Setup(testPlan(), r); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	want := []string{
		"CreateNetns ns-agent-1",
		"CreateVethPair veth-h1 veth-a1",
		"MoveToNetns veth-a1 ns-agent-1",
		"SetAddr host veth-h1 10.0.0.1/30",
		"SetLinkUp host veth-h1",
		"SetAddr ns-agent-1 veth-a1 10.0.0.2/30",
		"SetLinkUp ns-agent-1 veth-a1",
		"AddDefaultRoute ns-agent-1 10.0.0.1",
		"ApplyFence ns-agent-1",
		"CreateCgroup agent-1",
		"EnterAndExec ns-agent-1 agent-1 plexus,run,--id,agent-1,--sandbox",
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("call sequence:\n got  %v\n want %v", r.calls, want)
	}
	// ApplyFence received the plan's policy + params; the fence they render (the
	// golden GenerateNFT the real executor mirrors) is the real deny-all fence.
	if r.fenceParams != testPlan().NFT {
		t.Fatalf("fence params = %+v", r.fenceParams)
	}
	nft, err := netpol.GenerateNFT(r.fencePolicy, r.fenceParams)
	if err != nil {
		t.Fatalf("render fence: %v", err)
	}
	for _, sub := range []string{
		"policy drop",
		"ip daddr 10.0.0.1 tcp dport 4222 accept",
		"meta l4proto tcp",
		"meta mark set 0x1",
	} {
		if !strings.Contains(nft, sub) {
			t.Fatalf("fence missing %q:\n%s", sub, nft)
		}
	}
}

// Fail-closed: an invalid plan (CP not a bare IPv4 — the nft-injection vector) must
// fail during pure generation, before ANY kernel object is built.
func TestSetupFailClosed(t *testing.T) {
	p := testPlan()
	p.NFT.CP = "1.2.3.4 accept\n evil"
	r := &recorder{}
	if err := Setup(p, r); err == nil {
		t.Fatal("Setup must fail on an invalid CP")
	}
	if len(r.calls) != 0 {
		t.Fatalf("fail-closed: no kernel op before a valid fence, got %v", r.calls)
	}
}

// Fail-fast: an executor error aborts the sequence — the cgroup and the agent exec
// never run, so a half-built fence never launches an agent.
func TestSetupAbortsOnError(t *testing.T) {
	r := &recorder{failAt: "ApplyFence"}
	if err := Setup(testPlan(), r); err == nil {
		t.Fatal("Setup must propagate the executor error")
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "CreateCgroup") || strings.HasPrefix(c, "EnterAndExec") {
			t.Fatalf("must abort at ApplyFence, but ran %q", c)
		}
	}
	if last := r.calls[len(r.calls)-1]; !strings.HasPrefix(last, "ApplyFence") {
		t.Fatalf("last call should be the failed ApplyFence, got %q", last)
	}
}

// A deny-all policy still gets a fence applied (the nft policy-drop); its reroute is
// empty, which is the executor's concern (GenerateIPRules returns nil for it).
func TestSetupDenyAllStillFences(t *testing.T) {
	p := testPlan()
	p.Net = netpol.NetPolicy{} // zero = tcp/udp drop
	r := &recorder{}
	if err := Setup(p, r); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	nft, err := netpol.GenerateNFT(r.fencePolicy, r.fenceParams)
	if err != nil || !strings.Contains(nft, "policy drop") {
		t.Fatalf("deny-all fence must still be applied: err=%v\n%s", err, nft)
	}
	if rr, _ := netpol.GenerateIPRules(r.fencePolicy, r.fenceParams); rr != nil {
		t.Fatalf("deny-all needs no tproxy reroute, got %v", rr)
	}
}
