package brain

import (
	"strings"
	"testing"

	"plexus/pkg/effector"
)

func TestParseRoleCard(t *testing.T) {
	// A structured card parses; identity/function fields land and PermittedSet reflects the grant.
	card := "name: Dev\n" +
		"summary: an implementation agent.\n" +
		"responsibilities:\n  - write code\n  - run tests\n" +
		"constraints:\n  - never report done without a green build\n" +
		"permitted: [fs.read, exec.boxed]\n"
	rc, err := ParseRoleCard([]byte(card), "test")
	if err != nil {
		t.Fatalf("ParseRoleCard: %v", err)
	}
	if rc.Name != "Dev" || rc.Summary != "an implementation agent." {
		t.Fatalf("identity not parsed: %+v", rc)
	}
	if len(rc.Responsibilities) != 2 || len(rc.Constraints) != 1 {
		t.Fatalf("function fields not parsed: %+v", rc)
	}
	set, err := rc.PermittedSet()
	if err != nil {
		t.Fatalf("PermittedSet: %v", err)
	}
	if want := effector.NewEffectSet(effector.FSRead, effector.ExecBoxed); set != want {
		t.Fatalf("PermittedSet=%v want %v", set, want)
	}

	// RenderSystemPrompt assembles the structured fields in order: identity line,
	// responsibilities, then constraints (which reference the kernel above).
	rendered := rc.RenderSystemPrompt()
	for _, want := range []string{"You are Dev — an implementation agent.", "YOUR RESPONSIBILITIES:", "- write code", "CONSTRAINTS", "kernel principles above"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered prompt missing %q:\n%s", want, rendered)
		}
	}

	// A guidance-only card is the raw-prompt escape hatch: it renders verbatim.
	raw, err := ParseRoleCard([]byte("guidance: |\n  You are a pirate.\n"), "test")
	if err != nil {
		t.Fatalf("ParseRoleCard (guidance-only): %v", err)
	}
	if got := raw.RenderSystemPrompt(); got != "You are a pirate." {
		t.Fatalf("guidance-only render=%q want verbatim", got)
	}

	// Omitted permitted -> empty set (assembler falls back to DefaultPolicy).
	rc2, err := ParseRoleCard([]byte("name: Dev\nsummary: x\n"), "test")
	if err != nil {
		t.Fatalf("ParseRoleCard (no permitted): %v", err)
	}
	if set, _ := rc2.PermittedSet(); !set.IsEmpty() {
		t.Fatalf("omitted permitted should be ∅, got %v", set)
	}

	// An empty card (renders nothing) and unknown effect names are both rejected at parse time.
	if _, err := ParseRoleCard([]byte("permitted: [fs.read]\n"), "test"); err == nil {
		t.Fatal("empty card (no renderable content) should error")
	}
	if _, err := ParseRoleCard([]byte("summary: hi\npermitted: [fs.read, bogus.tag]\n"), "test"); err == nil {
		t.Fatal("unknown effect name should error at parse time")
	}
}
