package brain

import (
	"fmt"
	"os"

	yaml "gopkg.in/yaml.v3"
)

// RoleCard is the structured role-card (§5.7.2): a Hermes-style job charter the
// brain seeds at L1. v0 carries ONLY SystemPrompt; identity / responsibilities /
// tool-trimming are added as fields later WITHOUT changing the type — which is
// why it is a struct, not a bare string.
type RoleCard struct {
	SystemPrompt string `yaml:"system_prompt"`
}

// LoadRoleCard reads a role-card YAML file into a RoleCard. The brain seeds the
// SystemPrompt as the L1 (system) frame — content rendered into the highest
// authority layer, distinct from the layering mechanism and from the wire `role`
// field (§5.7.2).
func LoadRoleCard(path string) (RoleCard, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RoleCard{}, fmt.Errorf("read role card %q: %w", path, err)
	}
	var rc RoleCard
	if err := yaml.Unmarshal(data, &rc); err != nil {
		return RoleCard{}, fmt.Errorf("parse role card %q: %w", path, err)
	}
	if rc.SystemPrompt == "" {
		return RoleCard{}, fmt.Errorf("role card %q has empty system_prompt", path)
	}
	return rc, nil
}
