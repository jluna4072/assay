package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"harness/backend"
)

var workCommand = command{
	name:    "work",
	args:    "<id>[@version] --task <text>",
	summary: "Have a worker do a task, checking and retrying until every rule passes",
	details: `
Makes a workspace (a git worktree of the last commit, on a new branch),
then repeats until every rule of the contract passes or a limit is
reached: run the worker with the task, check the workspace, and, if rules
failed, run the worker again with what failed and why. Each attempt's
changes are committed to the branch. Only harness decides that the work is
accepted; the worker's own claims count for nothing. harness never merges
the branch: review it and merge it yourself.

The worker is a program configured under workers: in .harness/config.yaml.
It gets the task, the rules, and any failures on stdin, and runs in the
workspace.

Progress goes to stderr; the result goes to stdout. Ctrl-C stops the
worker, records the attempt as cancelled, and keeps the workspace.

Flags:
  --task <text>         What the worker should do (or --task-file)
  --task-file <path>    Read the task from a file ("-" for stdin)
  --worker <name>       Worker to use (default: default_worker)
  --max-attempts <n>    Stop after n attempts (default 3)
  --timeout <duration>  Stop after this long in all (default 30m)
  --quiet               Don't print the worker's output
  --json                Print the run as one JSON object instead of text
` + projectFlagHelp + `

Exit codes: 0 accepted, 1 rejected, timed out, or cancelled, 2 the worker or
a check couldn't run, 64 usage error.
`,
	run: runWork,
}

func runWork(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	quiet := fs.Bool("quiet", false, "")
	projectDir := fs.String("project", "", "")
	task := fs.String("task", "", "")
	taskFile := fs.String("task-file", "", "")
	worker := fs.String("worker", "", "")
	maxAttempts := fs.Int("max-attempts", backend.DefaultMaxAttempts, "")
	timeout := fs.Duration("timeout", backend.DefaultWorkTimeout, "")
	refs, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(refs) != 1 {
		fmt.Fprintln(e.Stderr, "harness work: name exactly one contract, like docs or docs@2")
		return ExitUsage
	}
	switch {
	case *task != "" && *taskFile != "":
		fmt.Fprintln(e.Stderr, "harness work: use --task or --task-file, not both")
		return ExitUsage
	case *taskFile == "-":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(e.Stderr, "harness work: read the task: %v\n", err)
			return ExitOperational
		}
		*task = string(data)
	case *taskFile != "":
		data, err := os.ReadFile(resolve(e, *taskFile))
		if err != nil {
			fmt.Fprintf(e.Stderr, "harness work: %v\n", err)
			return ExitOperational
		}
		*task = string(data)
	}
	if strings.TrimSpace(*task) == "" {
		fmt.Fprintln(e.Stderr, "harness work: say what to do with --task \"...\" or --task-file <path>")
		return ExitUsage
	}
	if *maxAttempts < 1 || *timeout < time.Second {
		fmt.Fprintln(e.Stderr, "harness work: --max-attempts must be at least 1 and --timeout at least 1s")
		return ExitUsage
	}
	id, version, err := parseRef(refs[0])
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness work: %v\n", err)
		return ExitUsage
	}
	s, code := openProject(e, cmd, *projectDir)
	if s == nil {
		return code
	}
	defer s.Close()
	if *asJSON {
		s.service.Keys = newKeys(e, false)
	}
	published, _, code := lookupVersion(e, cmd, s, id, version)
	if code != ExitOK {
		return code
	}

	ctx := e.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	printer := &workPrinter{w: e.Stderr, root: s.project.Root, quiet: *quiet}
	run, err := s.service.Work(ctx, backend.WorkOptions{
		ContractID: id, Version: published.Version, ProjectDir: s.project.Root,
		Task: *task, Worker: *worker, MaxAttempts: *maxAttempts, Timeout: *timeout,
	}, printer.event)
	if err != nil {
		fmt.Fprintf(e.Stderr, "harness work: %v\n", err)
		return ExitOperational
	}
	if *asJSON {
		writeJSON(e.Stdout, struct {
			Schema int     `json:"schema"`
			Run    runJSON `json:"run"`
		}{JSONSchema, toRunJSON(run)})
	} else {
		printWorkResult(e.Stdout, run, s.project.Root)
	}
	return workExit(run.Work.Outcome)
}

func workExit(outcome backend.WorkOutcome) int {
	switch outcome {
	case backend.WorkAccepted:
		return ExitOK
	case backend.WorkError, backend.WorkRunning:
		return ExitOperational
	}
	return ExitNegative
}

// workPrinter prints a work run's progress.
type workPrinter struct {
	w     io.Writer
	root  string
	quiet bool
}

func (p *workPrinter) event(event backend.Event) {
	run := event.Run
	switch event.Type {
	case backend.EventRunStarted:
		fmt.Fprintf(p.w, "work %s@%d · run %s · worker %s\n", run.ContractID, run.Version, shortID(run.ID), run.Work.Worker)
		fmt.Fprintf(p.w, "workspace %s · branch %s\n", relative(p.root, run.Work.Workspace), run.Work.Branch)
	case backend.EventRunFinished:
		fmt.Fprintln(p.w)
	case backend.EventAttemptStarted:
		fmt.Fprintf(p.w, "\nattempt %d of %d · running %s\n", event.Attempt, run.Work.MaxAttempts, run.Work.Worker)
	case backend.EventWorkerOutput:
		if !p.quiet {
			fmt.Fprintf(p.w, "  │ %s\n", event.Line)
		}
	case backend.EventWorkerFinished:
		fmt.Fprintf(p.w, "  %s\n", describeWorker(*event.Worker))
	case backend.EventAttemptFinished:
		attempt := run.Attempts[len(run.Attempts)-1]
		if attempt.Worker != nil && attempt.Worker.Commit != "" {
			fmt.Fprintf(p.w, "  committed the workspace as %s\n", shortCommit(attempt.Worker.Commit))
		}
		if attempt.Status != backend.Cancelled {
			printResults(p.w, attempt, false)
		}
	}
}

// describeWorker summarizes a worker's part of an attempt on one line.
func describeWorker(record backend.WorkerRun) string {
	var status string
	switch {
	case record.Cancelled:
		status = "cancelled"
	case record.TimedOut:
		status = "timed out"
	case record.Error != "" && record.Duration == 0:
		return fmt.Sprintf("%s couldn't run: %s", record.Worker, record.Error)
	default:
		status = fmt.Sprintf("exited %d", record.ExitCode)
	}
	line := fmt.Sprintf("%s %s after %s, %s output", record.Worker, status, formatDuration(record.Duration), formatSize(record.Output.Size))
	if !record.Sandboxed {
		line += ", unsandboxed"
	}
	if record.Commit != "" {
		line += " · committed " + shortCommit(record.Commit)
	}
	if record.Error != "" && record.Duration > 0 {
		line += " · " + record.Error
	}
	return line
}

func printWorkResult(w io.Writer, run backend.Run, root string) {
	work := run.Work
	verdict := strings.ToUpper(strings.ReplaceAll(string(work.Outcome), "_", " "))
	fmt.Fprintf(w, "%s · %s@%d · run %s · %s\n", verdict, run.ContractID, run.Version, shortID(run.ID), work.Reason)
	fmt.Fprintf(w, "branch %s · workspace %s\n", work.Branch, relative(root, work.Workspace))
	switch work.Outcome {
	case backend.WorkAccepted:
		fmt.Fprintf(w, "Review the changes with: git diff %s...%s\nThen merge the branch yourself; harness never merges.\n", shortCommit(run.Baseline), work.Branch)
	default:
		fmt.Fprintf(w, "See every attempt with: harness show %s\n", shortID(run.ID))
	}
}

// relative shows path relative to root when it's inside it.
func relative(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
