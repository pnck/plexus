package brain

import (
	"fmt"
	"os"
	"strings"

	"plexus/pkg/effector"

	yaml "gopkg.in/yaml.v3"
)

// RoleCard is the structured role-card (§5.7.2): a Hermes-style job charter the
// brain renders into its L1 (system) frame, ON TOP of the universal kernel
// principles. It is the persistent, identity-level analog of a delegation's
// per-task Briefing — RenderSystemPrompt() assembles the structured fields into
// one system prompt the same way briefingPrompt() does for a Briefing.
//
// Fields split by audience: the identity/function fields render INTO the prompt
// the LLM sees; Permitted drives the approval gate (E3.3), not prose; Description
// is orchestration metadata (when to route to this role) and is NOT sent to the
// LLM — it is for the E5/E6 selector.
//
// Division of labor with the kernel principles (principles.txt): the kernel owns
// the universal loop MECHANICS and safety invariants (authority layering, tool vs
// delegation, memory, planning, task-truth, secrets, injection). A role card states
// only this role's MISSION and specialization (identity, responsibilities,
// role-specific constraints, style) and must NOT restate those mechanics.
type RoleCard struct {
	// ── Identity (rendered into L1) ──
	Name    string `yaml:"name,omitempty"`    // role title: "Manager" / "Dev" / "Tester"
	Summary string `yaml:"summary,omitempty"` // one-paragraph inward identity ("you are X, …")

	// ── Function (rendered into L1) ──
	Responsibilities []string `yaml:"responsibilities,omitempty"` // what this role does
	Constraints      []string `yaml:"constraints,omitempty"`      // role-specific must/never (kernel still binds)
	// Guidance is the freeform working-style tail. When it is the ONLY field set it
	// renders verbatim — the raw-prompt escape hatch behind `--system` / `/system`.
	Guidance string `yaml:"guidance,omitempty"`

	// ── Authority (NOT prose; drives the gate, E3.3) ──
	// Permitted is the role's auto-allow effect grant, as canonical dotted effect
	// names (e.g. fs.read, exec.boxed). Omitted -> the assembler falls back to
	// effector.DefaultPolicy. Validated at parse time; the assembler turns it into a
	// PermittedPolicy.
	Permitted []string `yaml:"permitted,omitempty"`

	// ── Orchestration metadata (NOT sent to the LLM) ──
	// Description says when to route work to this role; it is for the E5/E6 selector,
	// not the agent's own prompt (mirrors a subagent's `description`).
	Description string `yaml:"description,omitempty"`
}

// RenderSystemPrompt assembles the structured fields into the role card's L1
// system-prompt text. The brain seeds this AFTER the kernel principles, so the
// card specializes on top of the universal invariants (hence the constraints note
// references "the kernel principles above"). An all-empty card renders "" — the
// signal to seed no role frame / fall back to a default.
func (r RoleCard) RenderSystemPrompt() string {
	var sb strings.Builder
	switch {
	case r.Name != "" && r.Summary != "":
		fmt.Fprintf(&sb, "You are %s — %s\n", r.Name, r.Summary)
	case r.Name != "":
		fmt.Fprintf(&sb, "You are %s.\n", r.Name)
	case r.Summary != "":
		fmt.Fprintf(&sb, "%s\n", r.Summary)
	}
	if len(r.Responsibilities) > 0 {
		sb.WriteString("\nYOUR RESPONSIBILITIES:\n")
		for _, x := range r.Responsibilities {
			fmt.Fprintf(&sb, "- %s\n", x)
		}
	}
	if len(r.Constraints) > 0 {
		sb.WriteString("\nCONSTRAINTS (role-specific — the kernel principles above still bind you):\n")
		for _, x := range r.Constraints {
			fmt.Fprintf(&sb, "- %s\n", x)
		}
	}
	if r.Guidance != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(r.Guidance)
	}
	return strings.TrimSpace(sb.String())
}

// IsZero reports whether the card has no renderable content — the signal for the
// assembler to fall back to a default card.
func (r RoleCard) IsZero() bool { return r.RenderSystemPrompt() == "" }

// PermittedSet parses Permitted into an EffectSet. Empty/nil yields the empty set
// (the assembler treats that as "use DefaultPolicy", not "permit nothing").
func (r RoleCard) PermittedSet() (effector.EffectSet, error) {
	return effector.ParseEffectSet(r.Permitted)
}

// ParseRoleCard parses role-card YAML bytes into a RoleCard, validating that the
// card renders a non-empty system prompt (it must state at least a summary,
// responsibilities, or guidance) and that every permitted entry is a known effect
// name. source labels errors (a file path, or "embedded ..."). LoadRoleCard and
// the chat embed share it so on-disk and embedded cards use one format (dogfood).
func ParseRoleCard(data []byte, source string) (RoleCard, error) {
	var rc RoleCard
	if err := yaml.Unmarshal(data, &rc); err != nil {
		return RoleCard{}, fmt.Errorf("parse role card %q: %w", source, err)
	}
	if rc.IsZero() {
		return RoleCard{}, fmt.Errorf("role card %q is empty: needs at least name/summary, responsibilities, or guidance", source)
	}
	if _, err := rc.PermittedSet(); err != nil {
		return RoleCard{}, fmt.Errorf("role card %q: %w", source, err)
	}
	return rc, nil
}

// LoadRoleCard reads a role-card YAML file into a RoleCard (via ParseRoleCard).
// The brain renders it into the L1 (system) frame — content rendered into the
// highest authority layer, distinct from the layering mechanism and from the wire
// `role` field (§5.7.2).
func LoadRoleCard(path string) (RoleCard, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RoleCard{}, fmt.Errorf("read role card %q: %w", path, err)
	}
	return ParseRoleCard(data, path)
}
