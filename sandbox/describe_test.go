package sandbox

import (
	"strings"
	"testing"

	"plexus/sandbox/bwrap"
	"plexus/sandbox/netpol"
)

// Environment.Describe composes every startup-fixed sandbox face (fs/namespace +
// network + resource limits) into one LLM-facing frame; the mechanism never leaks.
func TestEnvironmentDescribe(t *testing.T) {
	net := netpol.NetPolicy{TCP: netpol.Redirect, UDP: netpol.Drop}
	d := Environment{
		Policy: bwrap.Policy{
			System:    []string{"/"},
			Provision: bwrap.Provision{Workspace: bwrap.Bind{Src: "/h/ws", Dest: "/work"}},
		},
		Net:    &net,
		Limits: Rlimits{NPROC: 64, AS: 512 << 20},
	}.Describe()

	for _, want := range []string{
		"fixed at startup",
		"Writable paths: /work",
		"Read-only paths",
		"isolated pid/ipc/user",
		"Outbound network", "TCP:", "UDP:",
		"Resource limits", "up to 64 processes", "512MiB address space",
	} {
		if !strings.Contains(d, want) {
			t.Fatalf("Environment.Describe missing %q:\n%s", want, d)
		}
	}
	for _, bad := range []string{"tproxy", "TPROXY", "nft", "--bind", "IP_TRANSPARENT", "RLIMIT", "setrlimit"} {
		if strings.Contains(d, bad) {
			t.Fatalf("Describe must not leak mechanism %q:\n%s", bad, d)
		}
	}
}

// A nil Net means the network is unmanaged (host net) — the chat/single-process
// case; zero Limits contribute no line.
func TestEnvironmentDescribeUnmanaged(t *testing.T) {
	d := Environment{Policy: bwrap.Policy{System: []string{"/"}}}.Describe()
	if !strings.Contains(d, "host network, no per-agent restrictions") {
		t.Fatalf("nil Net must describe host network:\n%s", d)
	}
	if strings.Contains(d, "Outbound network:") && !strings.Contains(d, "host network") {
		t.Fatalf("nil Net should not render a fenced egress line:\n%s", d)
	}
	if strings.Contains(d, "Resource limits") {
		t.Fatalf("zero Limits must add no line:\n%s", d)
	}
}
