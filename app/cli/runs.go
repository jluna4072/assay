package cli

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"harness/backend"
)

const shortIDLength = 6

var checkCommand = command{
	name:    "check",
	args:    "<id>[@version]",
	summary: "Start a run of a published contract and check the project",
	details: `
Starts a run of a published contract against this project and records the
first attempt. Without @version, uses the latest version. The run stays tied
to that version even after newer versions are published.

Flags:
  --json            Print one JSON object instead of text
` + projectFlagHelp + `

Exit codes: 0 every rule passed, 1 a rule failed or was blocked, 2 a check
couldn't run, 64 usage error or unknown contract.
`,
	run: runCheck,
}

var recheckCommand = command{
	name:    "recheck",
	args:    "[run]",
	summary: "Check the project again, adding an attempt to a run",
	details: `
Records a new attempt for an existing run, using the run's published
version. Earlier attempts are kept. Without a run ID, uses the most recent
run. A run ID can be shortened to any unique prefix.

Flags:
  --json            Print one JSON object instead of text
` + projectFlagHelp + `

Exit codes: as for check.
`,
	run: runRecheck,
}

var runsCommand = command{
	name:    "runs",
	args:    "",
	summary: "List runs, newest first",
	details: `
Lists this project's runs, newest first.

Flags:
  --contract <id>   Only runs of this contract
  --limit <n>       Show at most n runs (default 20, 0 for all)
  --json            Print one JSON object instead of text
` + projectFlagHelp + `
`,
	run: runRuns,
}

var showRunCommand = command{
	name:    "show",
	args:    "[run]",
	summary: "Show a run's attempts and why each rule passed or failed",
	details: `
Prints every attempt of a run, oldest first, with each rule's result and
reason. Without a run ID, shows the most recent run. A run ID can be
shortened to any unique prefix.

Flags:
  --attempt <n>     Only attempt n
  --evidence        List the evidence recorded for each rule
  --json            Print one JSON object instead of text
` + projectFlagHelp + `
`,
	run: runShowRun,
}

// JSON shapes for runs. These are the documented --json output, separate
// from the backend's storage types so either can change independently.
type workJSON struct {
	Task        string     `json:"task"`
	Worker      string     `json:"worker"`
	Workspace   string     `json:"workspace"`
	Branch      string     `json:"branch"`
	MaxAttempts int        `json:"maxAttempts"`
	TimeoutMs   int64      `json:"timeoutMs"`
	Outcome     string     `json:"outcome"`
	Reason      string     `json:"reason,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

type workerJSON struct {
	Worker    string       `json:"worker"`
	Argv      []string     `json:"argv"`
	ExitCode  int          `json:"exitCode"`
	TimedOut  bool         `json:"timedOut,omitempty"`
	Cancelled bool         `json:"cancelled,omitempty"`
	Duration  int64        `json:"durationMs"`
	Sandboxed bool         `json:"sandboxed"`
	Prompt    evidenceJSON `json:"prompt"`
	Output    evidenceJSON `json:"output"`
	Commit    string       `json:"commit,omitempty"`
	Error     string       `json:"error,omitempty"`
}

type runJSON struct {
	ID         string        `json:"id"`
	ShortID    string        `json:"shortId"`
	Contract   string        `json:"contract"`
	Version    int           `json:"version"`
	ProjectDir string        `json:"projectDir"`
	Baseline   string        `json:"baseline,omitempty"`
	Status     string        `json:"status"`
	CreatedAt  time.Time     `json:"createdAt"`
	Attempts   []attemptJSON `json:"attempts"`
	Work       *workJSON     `json:"work,omitempty"`
}

type attemptJSON struct {
	Number      int          `json:"number"`
	Status      string       `json:"status"`
	StartedAt   time.Time    `json:"startedAt"`
	CompletedAt time.Time    `json:"completedAt"`
	Results     []resultJSON `json:"results"`
	Worker      *workerJSON  `json:"worker,omitempty"`
}

type resultJSON struct {
	Rule     string         `json:"rule"`
	Status   string         `json:"status"`
	Reason   string         `json:"reason,omitempty"`
	Evidence []evidenceJSON `json:"evidence,omitempty"`
	Review   *reviewJSON    `json:"review,omitempty"`
}

// evidenceJSON describes one recorded evidence item; "harness evidence"
// prints its content.
type evidenceJSON struct {
	Kind      string   `json:"kind"`
	Path      string   `json:"path,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Baseline  string   `json:"baseline,omitempty"`
	Files     int      `json:"files,omitempty"`
	Absent    bool     `json:"absent,omitempty"`
	Command   string   `json:"command,omitempty"`
	Reviewer  string   `json:"reviewer,omitempty"`
	Argv      []string `json:"argv,omitempty"`
	ExitCode  *int     `json:"exitCode,omitempty"`   // commands that ran
	Duration  *int64   `json:"durationMs,omitempty"` // commands that ran
	TimedOut  bool     `json:"timedOut,omitempty"`
	Sandboxed *bool    `json:"sandboxed,omitempty"` // commands that ran
	Size      int64    `json:"size"`
	SHA256    string   `json:"sha256,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func toEvidenceJSON(item backend.Evidence) evidenceJSON {
	out := evidenceJSON{
		Kind: string(item.Kind), Path: item.Path, Paths: item.Paths, Baseline: item.Baseline, Files: item.Files,
		Absent: item.Absent, Command: item.Command, Reviewer: item.Reviewer, Argv: item.Argv, TimedOut: item.TimedOut,
		Size: item.Size, SHA256: item.SHA256, Truncated: item.Truncated, Error: item.Error,
	}
	if item.Kind == backend.CommandEvidence && item.Error == "" {
		exitCode, duration, sandboxed := item.ExitCode, item.Duration, item.Sandboxed
		out.ExitCode, out.Duration, out.Sandboxed = &exitCode, &duration, &sandboxed
	}
	return out
}

func toRunJSON(run backend.Run) runJSON {
	out := runJSON{
		ID: strings.ToLower(run.ID), ShortID: shortID(run.ID), Contract: run.ContractID, Version: run.Version,
		ProjectDir: run.ProjectDir, Baseline: run.Baseline, Status: run.Status, CreatedAt: run.CreatedAt, Attempts: []attemptJSON{},
	}
	if w := run.Work; w != nil {
		out.Work = &workJSON{Task: w.Task, Worker: w.Worker, Workspace: w.Workspace, Branch: w.Branch, MaxAttempts: w.MaxAttempts, TimeoutMs: w.Timeout, Outcome: string(w.Outcome), Reason: w.Reason, FinishedAt: w.FinishedAt}
	}
	for _, a := range run.Attempts {
		attempt := attemptJSON{Number: a.Number, Status: string(a.Status), StartedAt: a.StartedAt, CompletedAt: a.CompletedAt, Results: []resultJSON{}}
		if w := a.Worker; w != nil {
			attempt.Worker = &workerJSON{
				Worker: w.Worker, Argv: w.Argv, ExitCode: w.ExitCode, TimedOut: w.TimedOut, Cancelled: w.Cancelled, Duration: w.Duration,
				Sandboxed: w.Sandboxed, Prompt: toEvidenceJSON(w.Prompt), Output: toEvidenceJSON(w.Output), Commit: w.Commit, Error: w.Error,
			}
		}
		for _, r := range a.Results {
			result := resultJSON{Rule: r.RuleID, Status: string(r.Status), Reason: r.Reason}
			if r.Review != nil {
				review := toReviewJSON(*r.Review)
				result.Review = &review
			}
			for _, item := range r.Evidence {
				result.Evidence = append(result.Evidence, toEvidenceJSON(item))
			}
			attempt.Results = append(attempt.Results, result)
		}
		out.Attempts = append(out.Attempts, attempt)
	}
	return out
}

func shortID(id string) string {
	if len(id) > shortIDLength {
		id = id[:shortIDLength]
	}
	return strings.ToLower(id)
}

// listRuns returns runs newest first. Storage orders by its timestamp text,
// which isn't strictly chronological within a second, so sort by time here.
func listRuns(s *session) ([]backend.Run, error) {
	runs, err := s.service.ListRuns()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
	return runs, nil
}

// findRun resolves a run ID prefix (case-insensitive), or the most recent run
// when prefix is empty. Failures are reported and returned as exit codes.
func findRun(e Env, cmd *command, s *session, prefix string) (backend.Run, int) {
	runs, err := listRuns(s)
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness %s: %v\n", cmd.name, err)
		return backend.Run{}, ExitOperational
	}
	if len(runs) == 0 {
		fmt.Fprintf(e.Stderr, "harness %s: no runs yet. Start one with \"harness check <contract>\".\n", cmd.name)
		return backend.Run{}, ExitUsage
	}
	var matches []backend.Run
	if prefix == "" {
		matches = runs[:1]
	} else {
		for _, run := range runs {
			if strings.HasPrefix(strings.ToUpper(run.ID), strings.ToUpper(prefix)) {
				matches = append(matches, run)
			}
		}
	}
	switch len(matches) {
	case 0:
		fmt.Fprintf(e.Stderr, "harness %s: no run matches %q. List runs with \"harness runs\".\n", cmd.name, prefix)
		return backend.Run{}, ExitUsage
	case 1:
		run, err := s.service.GetRun(matches[0].ID)
		if err != nil {
			fmt.Fprintf(e.Stderr, "harness %s: %v\n", cmd.name, err)
			return backend.Run{}, ExitOperational
		}
		return run, ExitOK
	default:
		fmt.Fprintf(e.Stderr, "harness %s: %q matches %d runs; use more characters:\n", cmd.name, prefix, len(matches))
		for _, run := range matches {
			fmt.Fprintf(e.Stderr, "  %s  %s@%d  %s\n", strings.ToLower(run.ID), run.ContractID, run.Version, run.CreatedAt.Local().Format("2006-01-02 15:04"))
		}
		return backend.Run{}, ExitUsage
	}
}

// attemptExit maps an attempt's status to the process exit code.
func attemptExit(status backend.ResultStatus) int {
	switch status {
	case backend.Passed:
		return ExitOK
	case backend.OperationalError:
		return ExitOperational
	default:
		return ExitNegative
	}
}

var statusMarks = map[backend.ResultStatus]string{
	backend.Cancelled:        "–",
	backend.Passed:           "✓",
	backend.Failed:           "✗",
	backend.Blocked:          "⊘",
	backend.OperationalError: "!",
}

// severity orders results so the ones needing attention come first.
var severity = map[backend.ResultStatus]int{
	backend.OperationalError: 0,
	backend.Failed:           1,
	backend.Blocked:          2,
	backend.Passed:           3,
}

// printResults lists an attempt's rule results, most severe first, with a
// line per recorded evidence item when withEvidence is set.
func printResults(w io.Writer, attempt backend.Attempt, withEvidence bool) {
	results := append([]backend.RuleResult(nil), attempt.Results...)
	sort.SliceStable(results, func(i, j int) bool { return severity[results[i].Status] < severity[results[j].Status] })
	width := 0
	for _, r := range results {
		width = max(width, len(r.RuleID))
	}
	for _, r := range results {
		mark := statusMarks[r.Status]
		if mark == "" {
			mark = "?"
		}
		reason := oneLine(r.Reason)
		if r.Status == backend.Blocked {
			reason = "blocked: " + reason
		}
		if reason == "" {
			fmt.Fprintf(w, "  %s %s\n", mark, r.RuleID)
		} else {
			fmt.Fprintf(w, "  %s %-*s   %s\n", mark, width, r.RuleID, reason)
		}
		if withEvidence {
			if r.Review != nil && r.Review.Error == "" {
				fmt.Fprintf(w, "      review by %s · %s\n", r.Review.Reviewer, describeReview(*r.Review))
			}
			for i, item := range r.Evidence {
				fmt.Fprintf(w, "      [%d] %s\n", i+1, describeEvidence(item))
			}
		}
	}
}

// describeEvidence summarizes an evidence item on one line.
func describeEvidence(item backend.Evidence) string {
	var subject string
	switch item.Kind {
	case backend.FileEvidence:
		subject = "file " + item.Path
	case backend.DiffEvidence:
		subject = "diff since " + shortCommit(item.Baseline)
		if len(item.Paths) > 0 {
			subject += " of " + strings.Join(item.Paths, ", ")
		}
	case backend.CommandEvidence:
		subject = "command " + item.Command
	case backend.ReviewRequestEvidence:
		subject = "review request to " + item.Reviewer
	case backend.ReviewResponseEvidence:
		subject = "review response from " + item.Reviewer
	default:
		subject = string(item.Kind)
	}
	switch {
	case item.Error != "":
		return subject + " · couldn't collect: " + item.Error
	case item.Absent:
		return subject + " · absent"
	}
	detail := formatSize(item.Size)
	switch item.Kind {
	case backend.DiffEvidence:
		detail = plural(item.Files, "file") + " changed, " + detail
	case backend.CommandEvidence:
		status := fmt.Sprintf("exit %d", item.ExitCode)
		if item.TimedOut {
			status = "timed out"
		}
		detail = fmt.Sprintf("%s in %s, %s output", status, formatDuration(item.Duration), detail)
		if !item.Sandboxed {
			detail += ", unsandboxed"
		}
	}
	if item.Truncated {
		detail += fmt.Sprintf(", first %s stored", formatSize(backend.MaxEvidenceBytes))
	}
	return fmt.Sprintf("%s · %s · sha256 %s", subject, detail, item.SHA256[:12])
}

func shortCommit(hash string) string {
	switch {
	case hash == backend.EmptyTree:
		return "the empty repository"
	case len(hash) > 7:
		return hash[:7]
	case hash == "":
		return "(no baseline)"
	}
	return hash
}

func formatDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}

func formatSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	}
}

// printAttempt prints the newest attempt of a run with a summary line.
func printAttempt(w io.Writer, run backend.Run) {
	attempt := run.Attempts[len(run.Attempts)-1]
	fmt.Fprintf(w, "%s@%d · run %s · attempt %d · %s\n\n", run.ContractID, run.Version, shortID(run.ID), attempt.Number, strings.ToUpper(string(attempt.Status)))
	printResults(w, attempt, false)
	counts := map[backend.ResultStatus]int{}
	for _, r := range attempt.Results {
		counts[r.Status]++
	}
	fmt.Fprintln(w)
	if attempt.Status == backend.Passed {
		fmt.Fprintf(w, "all %s passed\n", plural(len(attempt.Results), "rule"))
		return
	}
	var parts []string
	if n := counts[backend.OperationalError]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d couldn't be checked", n))
	}
	if n := counts[backend.Failed]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	if n := counts[backend.Blocked]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", n))
	}
	if n := counts[backend.Passed]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d passed", n))
	}
	fmt.Fprintf(w, "%s · fix, then: harness recheck %s\n", strings.Join(parts, ", "), shortID(run.ID))
}

func runCheck(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	refs, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(refs) != 1 {
		fmt.Fprintln(e.Stderr, "harness check: name exactly one contract, like docs or docs@2")
		return ExitUsage
	}
	id, version, err := parseRef(refs[0])
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness check: %v\n", err)
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	published, _, code := lookupVersion(e, cmd, s, id, version)
	if code != ExitOK {
		return code
	}
	run, err := s.service.StartRun(id, published.Version, s.project.Root)
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness check: %v\n", err)
		return ExitOperational
	}
	return evaluate(e, cmd, s, run, *asJSON)
}

func runRecheck(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	ids, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(ids) > 1 {
		fmt.Fprintln(e.Stderr, "harness recheck: name at most one run")
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	prefix := ""
	if len(ids) == 1 {
		prefix = ids[0]
	}
	run, code := findRun(e, cmd, s, prefix)
	if code != ExitOK {
		return code
	}
	return evaluate(e, cmd, s, run, *asJSON)
}

// evaluate records a new attempt for run and prints it.
func evaluate(e Env, cmd *command, s *session, run backend.Run, asJSON bool) int {
	if asJSON {
		s.service.Keys = newKeys(e, false) // never stop to ask when output is for a program
	}
	run, err := s.service.EvaluateAttempt(run.ID)
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness %s: %v\n", cmd.name, err)
		return ExitOperational
	}
	attempt := run.Attempts[len(run.Attempts)-1]
	if asJSON {
		writeJSON(e.Stdout, struct {
			Schema  int     `json:"schema"`
			Attempt int     `json:"attempt"`
			Run     runJSON `json:"run"`
		}{JSONSchema, attempt.Number, toRunJSON(run)})
	} else {
		printAttempt(e.Stdout, run)
	}
	return attemptExit(attempt.Status)
}

type runSummaryJSON struct {
	ID        string    `json:"id"`
	ShortID   string    `json:"shortId"`
	Contract  string    `json:"contract"`
	Version   int       `json:"version"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"createdAt"`
}

func runRuns(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	contract := fs.String("contract", "", "")
	limit := fs.Int("limit", 20, "")
	rest, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(e.Stderr, "harness runs: unexpected argument %q\n", rest[0])
		return ExitUsage
	}
	if *limit < 0 {
		fmt.Fprintln(e.Stderr, "harness runs: --limit must be 0 or more")
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	all, err := listRuns(s)
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness runs: %v\n", err)
		return ExitOperational
	}
	summaries := []runSummaryJSON{}
	for _, run := range all {
		if *contract != "" && run.ContractID != *contract {
			continue
		}
		if *limit > 0 && len(summaries) == *limit {
			break
		}
		full, err := s.service.GetRun(run.ID)
		if err != nil {
			fmt.Fprintf(e.Stderr, "harness runs: %v\n", err)
			return ExitOperational
		}
		summaries = append(summaries, runSummaryJSON{
			ID: strings.ToLower(run.ID), ShortID: shortID(run.ID), Contract: run.ContractID, Version: run.Version,
			Status: full.Status, Attempts: len(full.Attempts), CreatedAt: run.CreatedAt,
		})
	}
	if *asJSON {
		writeJSON(e.Stdout, struct {
			Schema int              `json:"schema"`
			Runs   []runSummaryJSON `json:"runs"`
		}{JSONSchema, summaries})
		return ExitOK
	}
	if len(summaries) == 0 {
		fmt.Fprintln(e.Stdout, "no runs yet. Start one with \"harness check <contract>\".")
		return ExitOK
	}
	table := tabwriter.NewWriter(e.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "RUN\tCONTRACT\tSTATUS\tATTEMPTS\tSTARTED")
	for _, r := range summaries {
		fmt.Fprintf(table, "%s\t%s@%d\t%s\t%d\t%s\n", r.ShortID, r.Contract, r.Version, r.Status, r.Attempts, r.CreatedAt.Local().Format("2006-01-02 15:04"))
	}
	table.Flush()
	return ExitOK
}

func runShowRun(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	projectDir := fs.String("project", "", "")
	only := fs.Int("attempt", 0, "")
	withEvidence := fs.Bool("evidence", false, "")
	ids, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(ids) > 1 {
		fmt.Fprintln(e.Stderr, "harness show: name at most one run")
		return ExitUsage
	}
	if *only < 0 {
		fmt.Fprintln(e.Stderr, "harness show: --attempt must be a positive number")
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()

	prefix := ""
	if len(ids) == 1 {
		prefix = ids[0]
	}
	run, code := findRun(e, cmd, s, prefix)
	if code != ExitOK {
		return code
	}
	attempts := run.Attempts
	if *only > 0 {
		if *only > len(run.Attempts) {
			fmt.Fprintf(e.Stderr, "harness show: run %s has %s\n", shortID(run.ID), plural(len(run.Attempts), "attempt"))
			return ExitUsage
		}
		attempts = run.Attempts[*only-1 : *only]
	}
	if *asJSON {
		filtered := run
		filtered.Attempts = attempts
		writeJSON(e.Stdout, struct {
			Schema int     `json:"schema"`
			Run    runJSON `json:"run"`
		}{JSONSchema, toRunJSON(filtered)})
		return ExitOK
	}

	fmt.Fprintf(e.Stdout, "run %s · %s@%d · %s\n", shortID(run.ID), run.ContractID, run.Version, run.ProjectDir)
	started := fmt.Sprintf("started %s", run.CreatedAt.Local().Format("2006-01-02 15:04"))
	if run.Baseline != "" {
		started += " at " + shortCommit(run.Baseline)
	}
	fmt.Fprintf(e.Stdout, "%s · %s\n", started, runStatusLine(run))
	if w := run.Work; w != nil {
		fmt.Fprintf(e.Stdout, "work by %s · %s", w.Worker, strings.ReplaceAll(string(w.Outcome), "_", " "))
		if w.Reason != "" {
			fmt.Fprintf(e.Stdout, ": %s", w.Reason)
		}
		fmt.Fprintf(e.Stdout, "\nbranch %s · workspace %s\ntask: %s\n", w.Branch, relative(run.ProjectDir, w.Workspace), oneLine(w.Task))
	}
	for _, attempt := range attempts {
		fmt.Fprintf(e.Stdout, "\nattempt %d · %s · %s\n", attempt.Number, attempt.Status, attempt.StartedAt.Local().Format("2006-01-02 15:04:05"))
		if attempt.Worker != nil {
			fmt.Fprintf(e.Stdout, "  %s\n", describeWorker(*attempt.Worker))
		}
		printResults(e.Stdout, attempt, *withEvidence)
	}
	if *withEvidence {
		fmt.Fprintf(e.Stdout, "\nPrint an item with: harness evidence %s <rule> [--attempt n] [--item n]\n", shortID(run.ID))
	}
	return ExitOK
}

func runStatusLine(run backend.Run) string {
	n := len(run.Attempts)
	if n == 0 {
		return "no attempts yet"
	}
	if run.Status == string(backend.Passed) {
		if n == 1 {
			return "passed on the first attempt"
		}
		return fmt.Sprintf("passed on attempt %d", n)
	}
	return fmt.Sprintf("%s after %s", run.Status, plural(n, "attempt"))
}
