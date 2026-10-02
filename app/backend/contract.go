package backend

import (
	"errors"
	"fmt"
	"os"
)

type RuleResult struct {
	RuleID string `json:"ruleId"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason"`
}

type RuleKind string

const FileExists RuleKind = "file_exists"

type Rule struct {
	ID        string   `json:"id"`
	Criterion string   `json:"criterion"`
	Kind      RuleKind `json:"kind"`
	Path      string   `json:"path"`
	DependsOn []string `json:"dependsOn"`
}

type Contract struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Rules []Rule `json:"rules"`
}

func CheckFileExists(projectDir string, rule Rule) (RuleResult, error) {
	result := RuleResult{RuleID: rule.ID}
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return result, fmt.Errorf("open project directory: %w", err)
	}
	defer root.Close()

	info, err := root.Stat(rule.Path)
	if errors.Is(err, os.ErrNotExist) {
		result.Reason = fmt.Sprintf("%s does not exist", rule.Path)
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("check %q: %w", rule.Path, err)
	}
	if !info.Mode().IsRegular() {
		result.Reason = fmt.Sprintf("%s is not a regular file", rule.Path)
		return result, nil
	}

	result.Passed = true
	return result, nil
}
