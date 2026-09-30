package main

import (
	"harness/backend"
)

func main() {
	contract := backend.Contract{
		ID: "help-me",
		Name: "Help me contract",
		Rules: []backend.Rule{
			{
			ID: "help-me-rule",
			Criterion: "project should contain README.md within the directory",
			Path: "README.md",
			},
		},
	}

	path := "/Users/jacobluna/harness/test"

	err := backend.CheckFileExists(path, contract.Rules[0])
	print(err)
	print(contract.Rules[0].RuleResult.Passed)
	
	
}