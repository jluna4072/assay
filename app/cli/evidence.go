package cli

import (
	"encoding/base64"
	"flag"
	"fmt"
	"strings"
	"unicode/utf8"

	"harness/backend"
)

var evidenceCommand = command{
	name:    "evidence",
	args:    "[run] <rule>",
	summary: "Print the evidence recorded for a rule",
	details: `
Prints what harness recorded for a rule when it checked it: file contents
and diffs, exactly as they were at that attempt. Later changes to the
project don't change recorded evidence. Without a run ID, uses the most
recent run; without --attempt, the latest attempt.

Each item is printed under a header line. With --item, only that item's
content is printed, with no header, so it can be piped or saved.

Flags:
  --attempt <n>     Use attempt n
  --item <n>        Print only item n (numbered as in "harness show --evidence")
  --json            Print one JSON object instead of text
` + projectFlagHelp + `

Exit codes: 0 printed, 1 the item is an absent file, 2 the item couldn't be
collected or its content can't be read, 64 usage error.
`,
	run: runEvidence,
}

type evidenceItemJSON struct {
	evidenceJSON
	Item          int    `json:"item"`
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"contentBase64,omitempty"`
}

func runEvidence(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	attemptNumber := fs.Int("attempt", 0, "")
	itemNumber := fs.Int("item", 0, "")
	positional, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	var prefix, ruleID string
	switch len(positional) {
	case 1:
		ruleID = positional[0]
	case 2:
		prefix, ruleID = positional[0], positional[1]
	default:
		fmt.Fprintln(e.Stderr, "harness evidence: name a rule, optionally after a run ID")
		return ExitUsage
	}
	if *attemptNumber < 0 || *itemNumber < 0 {
		fmt.Fprintln(e.Stderr, "harness evidence: --attempt and --item must be positive numbers")
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	run, code := findRun(e, cmd, s, prefix)
	if code != ExitOK {
		return code
	}
	if len(run.Attempts) == 0 {
		fmt.Fprintf(e.Stderr, "harness evidence: run %s has no attempts yet\n", shortID(run.ID))
		return ExitUsage
	}
	attempt := run.Attempts[len(run.Attempts)-1]
	if *attemptNumber > 0 {
		if *attemptNumber > len(run.Attempts) {
			fmt.Fprintf(e.Stderr, "harness evidence: run %s has %s\n", shortID(run.ID), plural(len(run.Attempts), "attempt"))
			return ExitUsage
		}
		attempt = run.Attempts[*attemptNumber-1]
	}
	var result *backend.RuleResult
	for i := range attempt.Results {
		if attempt.Results[i].RuleID == ruleID {
			result = &attempt.Results[i]
		}
	}
	if result == nil {
		fmt.Fprintf(e.Stderr, "harness evidence: %s@%d has no rule %q\n", run.ContractID, run.Version, ruleID)
		return ExitUsage
	}
	items := result.Evidence
	first := 1
	if *itemNumber > 0 {
		if *itemNumber > len(items) {
			fmt.Fprintf(e.Stderr, "harness evidence: rule %s has %s in attempt %d\n", ruleID, plural(len(items), "evidence item"), attempt.Number)
			return ExitUsage
		}
		items, first = items[*itemNumber-1:*itemNumber], *itemNumber
	}
	if len(items) == 0 {
		reason := "its contract asks for none"
		if result.Status == backend.Blocked {
			reason = "it was blocked, so it wasn't checked"
		}
		fmt.Fprintf(e.Stderr, "harness evidence: no evidence for rule %s in attempt %d: %s\n", ruleID, attempt.Number, reason)
		return ExitNegative
	}

	exit := ExitOK
	worse := func(c int) {
		if c == ExitOperational || exit == ExitOK {
			exit = c
		}
	}
	contents := make([][]byte, len(items))
	for i, item := range items {
		switch {
		case item.Error != "":
			worse(ExitOperational)
		case item.Absent:
			worse(ExitNegative)
		default:
			content, err := s.service.GetEvidence(item.Blob)
			if err != nil {
				fmt.Fprintf(e.Stderr, "harness evidence: read item %d: %v\n", first+i, err)
				worse(ExitOperational)
				continue
			}
			contents[i] = content
		}
	}

	if *asJSON {
		list := make([]evidenceItemJSON, len(items))
		for i, item := range items {
			list[i] = evidenceItemJSON{evidenceJSON: toEvidenceJSON(item), Item: first + i}
			if contents[i] != nil {
				if utf8.Valid(contents[i]) {
					list[i].Content = string(contents[i])
				} else {
					list[i].ContentBase64 = base64.StdEncoding.EncodeToString(contents[i])
				}
			}
		}
		writeJSON(e.Stdout, struct {
			Schema  int                `json:"schema"`
			Run     string             `json:"run"`
			Attempt int                `json:"attempt"`
			Rule    string             `json:"rule"`
			Items   []evidenceItemJSON `json:"items"`
		}{JSONSchema, strings.ToLower(run.ID), attempt.Number, ruleID, list})
		return exit
	}

	for i, item := range items {
		if *itemNumber == 0 {
			if i > 0 {
				fmt.Fprintln(e.Stdout)
			}
			fmt.Fprintf(e.Stdout, "==> [%d] %s <==\n", first+i, describeEvidence(item))
		} else if contents[i] == nil {
			fmt.Fprintf(e.Stderr, "harness evidence: %s\n", describeEvidence(item))
		}
		if contents[i] != nil {
			e.Stdout.Write(contents[i])
			if *itemNumber == 0 && len(contents[i]) > 0 && contents[i][len(contents[i])-1] != '\n' {
				fmt.Fprintln(e.Stdout)
			}
		}
	}
	return exit
}
