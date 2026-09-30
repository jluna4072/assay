package backend

import (
	// "encoding/json"
	// "fmt"
	"errors"
	"os"
)
type RuleResult struct {
	RuleID		string
	Passed 		bool
	Reason		string
}

type Rule struct {
	ID 			string
	Criterion 	string
	Path 		string
	RuleResult  RuleResult
}

type Contract struct {
	ID			string
	Name		string
	Rules		[]Rule
}

func CheckFileExists(projectDir string, rule Rule) (error) {
	fullDirect := projectDir + "/" + rule.Path

	meta, err := os.Stat(fullDirect)

	if errors.Is(err, os.ErrNotExist) {
		rule.RuleResult.Reason = rule.Path + " does not exit."
	}

	if err != nil {
		rule.RuleResult.Reason = "Cannot verify."
	}
	
	rule.RuleResult.Passed = !meta.IsDir()


	return nil
}







