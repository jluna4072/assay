package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"assay/backend"
	"assay/contractfile"
)

var diffCommand = command{
	name:    "contract diff",
	args:    "<file>",
	summary: "Show what publishing a contract file would change",
	details: `
Compares a contract file with the latest published version of its contract,
both written in the contract file format, and prints the lines that would
change. Nothing is published.

Flags:
  --json            Print one JSON object instead of text
` + projectFlagHelp + `

Exit codes: 0 the file matches the latest version, 1 publishing would
change something (or the file has problems), 2 unreadable file or
database, 64 usage error.
`,
	run: runDiff,
}

// diffLine is one line of a diff: ' ' unchanged, '-' only in the
// published version, '+' only in the file.
type diffLine struct {
	op   byte
	text string
}

func (l diffLine) String() string { return string(l.op) + " " + l.text }

// contractChange is what publishing a contract would change.
type contractChange struct {
	contract backend.Contract
	latest   *backend.PublishedContract // nil when nothing is published yet
	changed  bool
	lines    []diffLine
}

// diffContract compares a contract with its latest published version.
func diffContract(s *session, contract backend.Contract) (contractChange, error) {
	change := contractChange{contract: contract, changed: true}
	versions, err := s.service.ListVersions(contract.ID)
	if err != nil {
		return change, err
	}
	var before []byte
	if n := len(versions); n > 0 {
		change.latest = &versions[n-1]
		change.changed = !backend.SameContent(versions[n-1].Contract, contract)
		if before, err = contractfile.Marshal(versions[n-1].Contract); err != nil {
			return change, err
		}
	}
	after, err := contractfile.Marshal(contract)
	if err != nil {
		return change, err
	}
	change.lines = lineDiff(splitLines(string(before)), splitLines(string(after)))
	return change, nil
}

func splitLines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// lineDiff finds the longest common subsequence of lines and marks the
// rest as removed or added. Contracts are small, so quadratic is fine.
func lineDiff(a, b []string) []diffLine {
	n, m := len(a), len(b)
	common := make([][]int, n+1)
	for i := range common {
		common[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				common[i][j] = common[i+1][j+1] + 1
			} else {
				common[i][j] = max(common[i+1][j], common[i][j+1])
			}
		}
	}
	var lines []diffLine
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			lines = append(lines, diffLine{' ', a[i]})
			i, j = i+1, j+1
		case i < n && (j == m || common[i+1][j] >= common[i][j+1]):
			lines = append(lines, diffLine{'-', a[i]})
			i++
		default:
			lines = append(lines, diffLine{'+', b[j]})
			j++
		}
	}
	return lines
}

// diffContext is how many unchanged lines are kept around each change.
const diffContext = 3

// trimDiff keeps the changed lines and a few around each, replacing longer
// unchanged stretches with a "…" line.
func trimDiff(lines []diffLine) []diffLine {
	keep := make([]bool, len(lines))
	for i, line := range lines {
		if line.op != ' ' {
			for k := max(0, i-diffContext); k <= min(len(lines)-1, i+diffContext); k++ {
				keep[k] = true
			}
		}
	}
	var out []diffLine
	for i := 0; i < len(lines); {
		if keep[i] {
			out = append(out, lines[i])
			i++
			continue
		}
		end := i
		for end < len(lines) && !keep[end] {
			end++
		}
		if end-i == 1 { // a gap of one line is shorter shown
			out = append(out, lines[i])
		} else {
			out = append(out, diffLine{'.', ""})
		}
		i = end
	}
	return out
}

// diffHeader names the two sides of a contract's diff.
func diffHeader(change contractChange, path string) (before, after string) {
	before = "(not published)"
	if change.latest != nil {
		before = fmt.Sprintf("%s@%d (published %s)", change.latest.Contract.ID, change.latest.Version, change.latest.PublishedAt.Local().Format("2006-01-02 15:04"))
	}
	return before, path
}

func writeDiff(w io.Writer, change contractChange, path string) {
	before, after := diffHeader(change, path)
	fmt.Fprintf(w, "--- %s\n+++ %s\n", before, after)
	for _, line := range trimDiff(change.lines) {
		if line.op == '.' {
			fmt.Fprintln(w, "  …")
		} else {
			fmt.Fprintln(w, line)
		}
	}
}

func runDiff(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	files, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(files) != 1 {
		fmt.Fprintln(e.Stderr, "assay contract diff: name exactly one contract file")
		return ExitUsage
	}
	path := files[0]
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	result, err := findConfig(s.project.Root).check(resolve(e, path))
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay contract diff: %v\n", err)
		return ExitOperational
	}
	type diffJSON struct {
		Schema   int                    `json:"schema"`
		Contract string                 `json:"contract,omitempty"`
		Latest   int                    `json:"latest"` // 0 when nothing is published
		Changed  bool                   `json:"changed"`
		Diff     []string               `json:"diff"`
		Problems []contractfile.Problem `json:"problems,omitempty"`
	}
	if !result.Valid() {
		if *asJSON {
			writeJSON(e.Stdout, diffJSON{Schema: JSONSchema, Diff: []string{}, Problems: result.Problems})
		} else {
			printProblems(e, path, result.Problems)
		}
		return ExitNegative
	}
	change, err := diffContract(s, result.Contract)
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay contract diff: %v\n", err)
		return ExitOperational
	}
	exit := ExitOK
	if change.changed {
		exit = ExitNegative
	}
	if *asJSON {
		out := diffJSON{Schema: JSONSchema, Contract: result.Contract.ID, Changed: change.changed, Diff: []string{}}
		if change.latest != nil {
			out.Latest = change.latest.Version
		}
		if change.changed {
			for _, line := range change.lines {
				out.Diff = append(out.Diff, line.String())
			}
		}
		writeJSON(e.Stdout, out)
		return exit
	}
	if !change.changed {
		fmt.Fprintf(e.Stdout, "%s@%d unchanged: the file matches the latest published version\n", change.latest.Contract.ID, change.latest.Version)
		return exit
	}
	writeDiff(e.Stdout, change, path)
	return exit
}
