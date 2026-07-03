package bwrap

import (
	"reflect"
	"strings"
	"testing"
)

// DefaultPolicy is the sensible chat/dev sandboxed default: the whole host rootfs
// read-only, inherited env, plus the baked isolation invariants.
func TestDefaultPolicy(t *testing.T) {
	want := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--unshare-all", "--share-net",
		"--cap-drop", "ALL",
		"--die-with-parent",
	}
	if got := Translate(DefaultPolicy()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Translate(DefaultPolicy())=\n%v\nwant\n%v", got, want)
	}
}

// The invariants are always emitted and --unshare-net never is — plexus never
// wants a fresh, routeless netns; a sandboxed agent always inherits the prepared
// one (--share-net).
func TestTranslateInvariants(t *testing.T) {
	got := Translate(Policy{System: []string{"/usr"}})
	for _, want := range [][]string{
		{"--dev", "/dev"}, {"--proc", "/proc"}, {"--tmpfs", "/tmp"},
		{"--unshare-all"}, {"--share-net"},
		{"--cap-drop", "ALL"}, {"--die-with-parent"},
	} {
		if !containsSeq(got, want) {
			t.Fatalf("invariant %v missing: %v", want, got)
		}
	}
	if contains(got, "--unshare-net") {
		t.Fatalf("--unshare-net must never be emitted: %v", got)
	}
}

// The semantic faces lower correctly: ro base, provision (role card ro,
// workspace/home rw + chdir + HOME), masking, and the sealed env grant.
func TestTranslateSemantic(t *testing.T) {
	got := Translate(Policy{
		System: []string{"/usr", "/bin"},
		Provision: Provision{
			RoleCard:  Bind{Src: "/h/role.yaml"},
			Workspace: Bind{Src: "/h/ws"},
			Home:      Bind{Src: "/h/home"},
		},
		Mask:     []string{"/prod"},
		Clearenv: true,
		Env:      []EnvVar{{Key: "TOKEN", Value: "x"}},
	})
	for _, want := range [][]string{
		{"--ro-bind", "/usr", "/usr"},
		{"--ro-bind", "/bin", "/bin"},
		{"--ro-bind", "/h/role.yaml", RoleCardPath}, // role card read-only
		{"--bind", "/h/ws", WorkspaceDir}, {"--chdir", WorkspaceDir},
		{"--bind", "/h/home", HomeDir}, {"--setenv", "HOME", HomeDir},
		{"--tmpfs", "/prod"},
		{"--clearenv"}, {"--setenv", "TOKEN", "x"},
	} {
		if !containsSeq(got, want) {
			t.Fatalf("Translate missing %v in %v", want, got)
		}
	}
	// The role card must be read-only — never a writable --bind (self-escalation guard).
	if containsSeq(got, []string{"--bind", "/h/role.yaml", RoleCardPath}) {
		t.Fatalf("role card must be --ro-bind, not --bind: %v", got)
	}
}

// Policy.Describe renders the fs/namespace confinement for the env-state frame —
// sandbox-side paths and isolation, never bwrap flags (E4.5).
func TestPolicyDescribe(t *testing.T) {
	d := Policy{
		System: []string{"/usr", "/bin"},
		Provision: Provision{
			RoleCard:  Bind{Src: "/h/r"},
			State:     Bind{Src: "/h/s"},
			Workspace: Bind{Src: "/h/ws"},
			Home:      Bind{Src: "/h/home"},
		},
		Mask: []string{"/prod"},
	}.Describe()
	for _, want := range []string{
		"Writable paths: " + StateDir + ", " + WorkspaceDir + ", " + HomeDir,
		"Read-only paths (you cannot modify these): /usr, /bin, " + RoleCardPath,
		"Ephemeral in-memory paths (cleared on exit): /tmp, /prod",
		"Working directory: " + WorkspaceDir,
		"isolated pid/ipc/user namespaces",
	} {
		if !strings.Contains(d, want) {
			t.Fatalf("Policy.Describe missing %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "--bind") || strings.Contains(d, "ro-bind") {
		t.Fatalf("Describe must not leak bwrap flags:\n%s", d)
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
