package backend

import (
	"errors"
	"fmt"
	"os"
)

type RuleResult struct {
	RuleID   string       `json:"ruleId"`
	Passed   bool         `json:"passed"`
	Reason   string       `json:"reason"`
	Status   ResultStatus `json:"status"`
	Evidence []Evidence   `json:"evidence,omitempty"`
}

type RuleKind string

const (
	// FileExists passes when Path is a regular file in the project.
	FileExists RuleKind = "file_exists"
	// CommandSucceeds passes when the approved command named by Command
	// exits with status 0.
	CommandSucceeds RuleKind = "command_succeeds"
)

type Rule struct {
	ID        string         `json:"id"`
	Criterion string         `json:"criterion"`
	Kind      RuleKind       `json:"kind"`
	Path      string         `json:"path"`
	Command   string         `json:"command,omitempty"`
	DependsOn []string       `json:"dependsOn"`
	Evidence  []EvidenceSpec `json:"evidence,omitempty"`
}

type Contract struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Rules []Rule `json:"rules"`
}

func CheckFileExists(projectDir string, rule Rule) (RuleResult, error) {
	result := RuleResult{RuleID: rule.ID, Status: OperationalError}
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return result, fmt.Errorf("open project directory: %w", err)
	}
	defer root.Close()

	info, err := root.Stat(rule.Path)
	if errors.Is(err, os.ErrNotExist) {
		result.Status = Failed
		result.Reason = fmt.Sprintf("%s does not exist", rule.Path)
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("check %q: %w", rule.Path, err)
	}
	if !info.Mode().IsRegular() {
		result.Status = Failed
		result.Reason = fmt.Sprintf("%s is not a regular file", rule.Path)
		return result, nil
	}

	result.Passed = true
	result.Status = Passed
	return result, nil
}
