package cli

import (
	"flag"
	"fmt"

	"harness/contractfile"
)

var validateCommand = command{
	name:    "contract validate",
	args:    "<file>...",
	summary: "Check contract files without publishing them",
	details: `
Reads each YAML contract file and reports every problem that would stop it
from being published: unknown or duplicate keys, wrong types, missing fields,
unsupported checks, paths outside the project, unknown or duplicate
dependencies, and dependency cycles. Nothing is saved.

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

func runValidate(e env, cmd *command, args []string) int {
	fs := flag.NewFlagSet("contract validate", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	files, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(files) == 0 {
		fmt.Fprintln(e.stderr, "harness contract validate: name at least one contract file")
		return ExitUsage
	}

	exit := ExitOK
	valid, invalid, unreadable := 0, 0, 0
	reports := make([]validateFileJSON, 0, len(files))
	for _, path := range files {
		report := validateFileJSON{Path: path, Problems: []contractfile.Problem{}}
		result, err := contractfile.Check(path)
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
		writeJSON(e.stdout, struct {
			Schema int                `json:"schema"`
			Valid  bool               `json:"valid"`
			Files  []validateFileJSON `json:"files"`
		}{JSONSchema, exit == ExitOK, reports})
		return exit
	}

	for _, report := range reports {
		switch {
		case report.Error != "":
			fmt.Fprintf(e.stderr, "harness: %s\n", report.Error)
		case report.Valid:
			fmt.Fprintf(e.stdout, "✓ %s: %s, %s\n", report.Path, report.Contract.ID, plural(report.Contract.Rules, "rule"))
		default:
			fmt.Fprintf(e.stdout, "✗ %s: %s\n", report.Path, plural(len(report.Problems), "problem"))
			for _, problem := range report.Problems {
				fmt.Fprintf(e.stdout, "  %s:%s\n", report.Path, problem)
			}
		}
	}
	if len(files) > 1 {
		summary := fmt.Sprintf("%d valid, %d invalid", valid, invalid)
		if unreadable > 0 {
			summary += fmt.Sprintf(", %d unreadable", unreadable)
		}
		fmt.Fprintln(e.stdout, summary)
	}
	return exit
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
