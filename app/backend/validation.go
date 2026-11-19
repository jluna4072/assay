package backend

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ValidationError is one reason a contract can't be published. Rule is the
// index into Contract.Rules, or -1 for a contract-level field, so callers that
// know where each rule came from (such as a file loader) can point at it.
type ValidationError struct {
	Rule    int
	RuleID  string
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.RuleID != "" {
		return fmt.Sprintf("rule %q: %s", e.RuleID, e.Message)
	}
	if e.Rule >= 0 {
		return fmt.Sprintf("rule %d: %s", e.Rule+1, e.Message)
	}
	return e.Message
}

// ValidationErrors unpacks every *ValidationError joined into err.
func ValidationErrors(err error) []*ValidationError {
	var result []*ValidationError
	var walk func(error)
	walk = func(err error) {
		if single, ok := err.(*ValidationError); ok {
			result = append(result, single)
			return
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, inner := range joined.Unwrap() {
				walk(inner)
			}
		}
	}
	if err != nil {
		walk(err)
	}
	return result
}

// ValidateContract checks publication requirements and reports every problem
// it finds, joined into one error. Drafts may be incomplete.
func ValidateContract(contract Contract) error {
	var problems []error
	report := func(rule int, ruleID, field, format string, args ...any) {
		problems = append(problems, &ValidationError{Rule: rule, RuleID: ruleID, Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if strings.TrimSpace(contract.ID) == "" {
		report(-1, "", "id", "contract ID is required")
	}
	if strings.TrimSpace(contract.Name) == "" {
		report(-1, "", "name", "contract name is required")
	}
	if len(contract.Rules) == 0 {
		report(-1, "", "rules", "a contract needs at least one rule")
	}

	index := make(map[string]int, len(contract.Rules))
	for i, rule := range contract.Rules {
		if strings.TrimSpace(rule.ID) == "" {
			report(i, "", "id", "rule ID is required")
		} else if first, exists := index[rule.ID]; exists {
			report(i, rule.ID, "id", "duplicate rule ID (first used by rule %d)", first+1)
		} else {
			index[rule.ID] = i
		}
		if strings.TrimSpace(rule.Criterion) == "" {
			report(i, rule.ID, "criterion", "acceptance criterion is required")
		}
		if rule.Kind != FileExists {
			report(i, rule.ID, "check", "unsupported check %q", rule.Kind)
		}
		if strings.TrimSpace(rule.Path) == "" || !filepath.IsLocal(rule.Path) || strings.Contains(rule.Path, "\\") {
			report(i, rule.ID, "path", "path must be a relative path inside the project")
		}
	}

	for i, rule := range contract.Rules {
		seen := make(map[string]bool)
		for _, dependency := range rule.DependsOn {
			if _, exists := index[dependency]; !exists {
				report(i, rule.ID, "depends_on", "dependency %q does not exist", dependency)
			} else if seen[dependency] {
				report(i, rule.ID, "depends_on", "duplicate dependency %q", dependency)
			}
			seen[dependency] = true
		}
	}

	// Report each dependency cycle once, at the rule where the search closes it.
	const (
		unvisited = iota
		active
		done
	)
	state := make(map[string]int)
	var stack []string
	var visit func(string)
	visit = func(id string) {
		state[id] = active
		stack = append(stack, id)
		for _, dependency := range contract.Rules[index[id]].DependsOn {
			if _, exists := index[dependency]; !exists {
				continue
			}
			switch state[dependency] {
			case unvisited:
				visit(dependency)
			case active:
				start := len(stack) - 1
				for stack[start] != dependency {
					start--
				}
				cycle := append(append([]string{}, stack[start:]...), dependency)
				report(index[id], id, "depends_on", "dependency cycle %s", strings.Join(cycle, " → "))
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = done
	}
	for i, rule := range contract.Rules {
		if first, indexed := index[rule.ID]; indexed && first == i && state[rule.ID] == unvisited {
			visit(rule.ID)
		}
	}

	return errors.Join(problems...)
}
