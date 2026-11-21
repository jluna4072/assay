package cli

import (
	"errors"
	"flag"
	"fmt"

	"harness/backend"
	"harness/contractfile"
	"harness/project"
)

var validateCommand = command{
	name:    "contract validate",
	args:    "<file>...",
	summary: "Check contract files without publishing them",
	details: `
Reads each YAML contract file and reports every problem that would stop it
from being published: unknown or duplicate keys, wrong types, missing fields,
unsupported checks, paths outside the project, unknown or duplicate
dependencies, and dependency cycles. Inside a harness project, it also checks
that every command the contract runs is approved in .harness/config.yaml.
Nothing is saved.

Flags:
  --json   Print one JSON object instead of text

Exit codes:
  0    every file is valid
  1    at least one file has problems
  2    a file couldn't be read
  64   usage error
`,
	run: runValidate,
}

type validateFileJSON struct {
	Path     string                 `json:"path"`
	Valid    bool                   `json:"valid"`
	Contract *contractSummaryJSON   `json:"contract,omitempty"`
	Problems []contractfile.Problem `json:"problems"`
	Error    string                 `json:"error,omitempty"`
}

type contractSummaryJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Rules int    `json:"rules"`
}

func runValidate(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet("contract validate", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	files, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(files) == 0 {
		fmt.Fprintln(e.Stderr, "harness contract validate: name at least one contract file")
		return ExitUsage
	}

	exit := ExitOK
	valid, invalid, unreadable := 0, 0, 0
	reports := make([]validateFileJSON, 0, len(files))
	// Inside a project, also check that commands are approved and reviewers
	// configured in its config.
	var config *backend.Config
	var configErr error
	if p, err := project.Find(e.Dir); err == nil {
		loaded, err := project.LoadConfig(p.Root)
		config, configErr = &loaded, err
	}
	for _, path := range files {
		report := validateFileJSON{Path: path, Problems: []contractfile.Problem{}}
		result, err := contractfile.Check(resolve(e, path))
		if err == nil && result.Valid() && usesConfig(result.Contract) {
			switch {
			case configErr != nil:
				err = configErr
			case config != nil:
				result.AddValidation(errors.Join(backend.ValidateCommands(result.Contract, *config), backend.ValidateReviewers(result.Contract, *config)))
			}
		}
		switch {
		case err != nil:
			report.Error = err.Error()
			exit = ExitOperational
			unreadable++
		case !result.Valid():
			report.Problems = result.Problems
			invalid++
			if exit == ExitOK {
				exit = ExitNegative
			}
		default:
			report.Valid = true
			valid++
			report.Contract = &contractSummaryJSON{ID: result.Contract.ID, Name: result.Contract.Name, Rules: len(result.Contract.Rules)}
		}
		reports = append(reports, report)
	}

	if *asJSON {
		writeJSON(e.Stdout, struct {
			Schema int                `json:"schema"`
			Valid  bool               `json:"valid"`
			Files  []validateFileJSON `json:"files"`
		}{JSONSchema, exit == ExitOK, reports})
		return exit
	}

	for _, report := range reports {
		switch {
		case report.Error != "":
			fmt.Fprintf(e.Stderr, "harness: %s\n", report.Error)
		case report.Valid:
			fmt.Fprintf(e.Stdout, "✓ %s: %s, %s\n", report.Path, report.Contract.ID, plural(report.Contract.Rules, "rule"))
		default:
			printProblems(e, report.Path, report.Problems)
		}
	}
	if len(files) > 1 {
		summary := fmt.Sprintf("%d valid, %d invalid", valid, invalid)
		if unreadable > 0 {
			summary += fmt.Sprintf(", %d unreadable", unreadable)
		}
		fmt.Fprintln(e.Stdout, summary)
	}
	return exit
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// printProblems lists a contract file's problems as path:line: message.
func printProblems(e Env, path string, problems []contractfile.Problem) {
	fmt.Fprintf(e.Stdout, "✗ %s: %s\n", path, plural(len(problems), "problem"))
	for _, problem := range problems {
		fmt.Fprintf(e.Stdout, "  %s:%s\n", path, problem)
	}
}

// usesConfig reports whether a contract depends on the project's config:
// approved commands or reviewers.
func usesConfig(contract backend.Contract) bool {
	for _, rule := range contract.Rules {
		if rule.Kind == backend.CommandSucceeds || rule.Kind == backend.ReviewCheck {
			return true
		}
		for _, spec := range rule.Evidence {
			if spec.Kind == backend.CommandEvidence {
				return true
			}
		}
	}
	return false
}
