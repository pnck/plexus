package bwrap

import (
	"reflect"
	"testing"
)

// DefaultPolicy must translate to E0's exact former hardcoded args, so routing
// Enter through the translation layer (E4.2) is behavior-preserving.
func TestDefaultPolicyBehaviorPreserving(t *testing.T) {
	want := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--unshare-all",
		"--share-net",
	}
	if got := Translate(DefaultPolicy()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Translate(DefaultPolicy())=\n%v\nwant\n%v", got, want)
	}
}

func TestTranslateFaces(t *testing.T) {
	// confine — net off = UnshareAll && !ShareNet -> no --share-net.
	off := Translate(Policy{UnshareAll: true, ShareNet: false})
	if contains(off, "--share-net") {
		t.Fatalf("net-off policy must not emit --share-net: %v", off)
	}
	if !contains(off, "--unshare-all") {
		t.Fatalf("expected --unshare-all: %v", off)
	}

	// provision (--role-card read-only inject + writable workspace) + ambient.
	got := Translate(Policy{
		ROBinds:       []Bind{{Src: "/host/agents/a/role.yaml", Dest: "/plexus/role.yaml"}},
		Binds:         []Bind{{Src: "/host/agents/a/ws", Dest: "/work"}},
		DieWithParent: true,
	})
	for _, want := range [][]string{
		{"--ro-bind", "/host/agents/a/role.yaml", "/plexus/role.yaml"},
		{"--bind", "/host/agents/a/ws", "/work"},
		{"--die-with-parent"},
	} {
		if !containsSeq(got, want) {
			t.Fatalf("Translate missing %v in %v", want, got)
		}
	}

	// The zero policy emits nothing (build outward from DefaultPolicy).
	if got := Translate(Policy{}); len(got) != 0 {
		t.Fatalf("zero Policy should translate to no args, got %v", got)
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func containsSeq(s, sub []string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if reflect.DeepEqual(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}
