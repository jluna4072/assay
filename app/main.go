package main

import (
	"fmt"
	"log"

	"harness/backend"
)

func main() {
	contract := backend.Contract{
		ID:   "help-me",
		Name: "Help me contract",
		Rules: []backend.Rule{
			{
				ID:        "help-me-rule",
				Criterion: "project should contain README.md within the directory",
				Path:      "README.md",
			},
		},
	}

	result, err := backend.CheckFileExists("../test", contract.Rules[0])
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("rule %s passed: %t\n", result.RuleID, result.Passed)
	if !result.Passed {
		fmt.Println(result.Reason)
	}
}
