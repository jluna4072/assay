package backend

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateContract checks publication requirements. Drafts may be incomplete.
func ValidateContract(contract Contract) error {
	if strings.TrimSpace(contract.ID) == "" || strings.TrimSpace(contract.Name) == "" {
		return fmt.Errorf("contract ID and name are required")
	}
	if len(contract.Rules) == 0 {
		return fmt.Errorf("a contract needs at least one rule")
	}
	rules := make(map[string]Rule, len(contract.Rules))
	for _, rule := range contract.Rules {
		if strings.TrimSpace(rule.ID) == "" || strings.TrimSpace(rule.Criterion) == "" {
			return fmt.Errorf("every rule needs an ID and acceptance criterion")
		}
		if _, exists := rules[rule.ID]; exists {
			return fmt.Errorf("duplicate rule ID %q", rule.ID)
		}
		if rule.Kind != FileExists {
			return fmt.Errorf("rule %q: unsupported check %q", rule.ID, rule.Kind)
		}
		if strings.TrimSpace(rule.Path) == "" || !filepath.IsLocal(rule.Path) || strings.Contains(rule.Path, "\\") {
			return fmt.Errorf("rule %q: path must be a relative path inside the project", rule.ID)
		}
		rules[rule.ID] = rule
	}
	for _, rule := range contract.Rules {
		seen := make(map[string]bool)
		for _, dependency := range rule.DependsOn {
			if _, exists := rules[dependency]; !exists {
				return fmt.Errorf("rule %q: dependency %q does not exist", rule.ID, dependency)
			}
			if seen[dependency] {
				return fmt.Errorf("rule %q: duplicate dependency %q", rule.ID, dependency)
			}
			seen[dependency] = true
		}
	}
	state := make(map[string]int)
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("dependency cycle includes rule %q", id)
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, dependency := range rules[id].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, rule := range contract.Rules {
		if err := visit(rule.ID); err != nil {
			return err
		}
	}
	return nil
}
