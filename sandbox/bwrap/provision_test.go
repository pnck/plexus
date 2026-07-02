package bwrap

import "testing"

func TestProvisionAddTo(t *testing.T) {
	p := DefaultPolicy()
	Provision{
		RoleCard:  Bind{Src: "/host/dev/role.yaml"},              // default Dest -> RoleCardPath
		State:     Bind{Src: "/host/dev/state"},                  // default Dest -> StateDir
		Workspace: Bind{Src: "/host/dev/ws", Dest: "/workspace"}, // manual Dest override
		Home:      Bind{Src: "/host/dev/home"},                   // default Dest -> HomeDir
	}.AddTo(&p)
	args := Translate(p)

	for _, want := range [][]string{
		{"--ro-bind", "/host/dev/role.yaml", RoleCardPath}, // role card: read-only, default path
		{"--bind", "/host/dev/state", StateDir},            // private DB: read-write, default path
		{"--bind", "/host/dev/ws", "/workspace"},           // workspace: MANUAL Dest honored
		{"--chdir", "/workspace"},                          // chdir follows the (manual) workspace Dest
		{"--bind", "/host/dev/home", HomeDir},              // writable HOME, default path
		{"--setenv", "HOME", HomeDir},                      // HOME env follows the home Dest
	} {
		if !containsSeq(args, want) {
			t.Fatalf("provision missing %v in %v", want, args)
		}
	}
	// The role card must be read-only — never a writable --bind (self-escalation guard).
	if containsSeq(args, []string{"--bind", "/host/dev/role.yaml", RoleCardPath}) {
		t.Fatalf("role card must be --ro-bind, not --bind: %v", args)
	}
	// An empty Provision contributes nothing.
	base := DefaultPolicy()
	before := len(Translate(base))
	Provision{}.AddTo(&base)
	if len(Translate(base)) != before {
		t.Fatalf("empty Provision must add no args")
	}
}
