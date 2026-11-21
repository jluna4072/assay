package backend

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Cancelled is the status of an attempt stopped before it finished, such
// as by Ctrl-C during "assay work".
const Cancelled ResultStatus = "cancelled"

// WorkOutcome is how a work run ended. Only assay sets Accepted, and only
// when every rule passed.
type WorkOutcome string

const (
	WorkRunning   WorkOutcome = "running"
	WorkAccepted  WorkOutcome = "accepted"  // every rule passed
	WorkRejected  WorkOutcome = "rejected"  // the attempt limit was reached
	WorkTimedOut  WorkOutcome = "timed_out" // the run's time limit was reached
	WorkCancelled WorkOutcome = "cancelled" // stopped by the user
	WorkError     WorkOutcome = "error"     // the worker or assay failed
)

// Defaults for "assay work".
const (
	DefaultMaxAttempts   = 3
	DefaultWorkTimeout   = 30 * time.Minute
	DefaultWorkerTimeout = 20 * time.Minute
)

// Work describes a run started by "assay work".
type Work struct {
	Task        string      `json:"task"`
	Worker      string      `json:"worker"`
	Workspace   string      `json:"workspace"` // a git worktree under .assay/workspaces
	Branch      string      `json:"branch"`    // the worktree's branch, which holds each attempt's commit
	MaxAttempts int         `json:"maxAttempts"`
	Timeout     int64       `json:"timeoutMs"`
	Outcome     WorkOutcome `json:"outcome"`
	Reason      string      `json:"reason,omitempty"` // why it ended, in words
	FinishedAt  *time.Time  `json:"finishedAt,omitempty"`
}

// WorkerSpec is a worker from the project's local config: a program that
// gets a task on stdin and changes files in the workspace it runs in.
type WorkerSpec struct {
	Name    string
	Command CommandSpec // run, timeout (per attempt), network, env, caches
	// Sandbox runs the worker under assay's sandbox: it can write only
	// to the workspace, a scratch directory, and its caches. Workers that
	// sandbox themselves, such as Codex, turn it off, because macOS
	// sandboxes can't be nested.
	Sandbox bool
}

// WorkerRun records the worker's part of one attempt.
type WorkerRun struct {
	Worker    string   `json:"worker"`
	Argv      []string `json:"argv"`
	ExitCode  int      `json:"exitCode"`
	TimedOut  bool     `json:"timedOut,omitempty"`
	Cancelled bool     `json:"cancelled,omitempty"`
	Duration  int64    `json:"durationMs"`
	Sandboxed bool     `json:"sandboxed"`
	Prompt    Evidence `json:"prompt"`           // exactly what the worker was given
	Output    Evidence `json:"output"`           // its combined stdout and stderr
	Commit    string   `json:"commit,omitempty"` // the commit assay made of the workspace afterwards
	Error     string   `json:"error,omitempty"`  // why it couldn't run
}

// Worker returns the worker a run names, or the default worker when name
// is empty.
func (c Config) Worker(name string) (WorkerSpec, error) {
	if name == "" {
		name = c.DefaultWorker
		if name == "" {
			switch len(c.Workers) {
			case 0:
				return WorkerSpec{}, errors.New("no workers are configured in .assay/config.yaml")
			case 1:
				for _, only := range c.Workers {
					return only, nil
				}
			default:
				return WorkerSpec{}, errors.New("name a worker with --worker, or set default_worker in .assay/config.yaml")
			}
		}
	}
	spec, ok := c.Workers[name]
	if !ok {
		return WorkerSpec{}, fmt.Errorf("worker %q isn't configured in .assay/config.yaml", name)
	}
	return spec, nil
}

// WorkOptions starts a work run.
type WorkOptions struct {
	ContractID  string
	Version     int
	ProjectDir  string
	Task        string
	Worker      string // "" for the default worker
	MaxAttempts int
	Timeout     time.Duration
}

// EventType names a step of a work run.
type EventType string

const (
	EventRunStarted      EventType = "run_started"
	EventAttemptStarted  EventType = "attempt_started"
	EventWorkerOutput    EventType = "worker_output"
	EventWorkerFinished  EventType = "worker_finished"
	EventAttemptFinished EventType = "attempt_finished"
	EventRunFinished     EventType = "run_finished"
)

// Event reports progress of a work run. The command line prints events;
// other front ends can show them their own way.
type Event struct {
	Seq     int         `json:"seq,omitempty"` // the event's number in its run, once stored
	Type    EventType   `json:"type"`
	Time    time.Time   `json:"time"`
	Run     *Run        `json:"-"`                 // the run as of this event
	Attempt int         `json:"attempt,omitempty"` // 1-based; for run_started, the attempts made before (a resumed run)
	Line    string      `json:"line,omitempty"`    // worker_output: one line
	Worker  *WorkerRun  `json:"worker,omitempty"`  // worker_finished
	Outcome WorkOutcome `json:"outcome,omitempty"` // run_finished
	Reason  string      `json:"reason,omitempty"`  // run_finished
}

// Work makes a workspace for the project, then runs the worker and checks
// its work until every rule passes or a limit is reached. Events reports
// progress; it may be nil. Cancelling ctx stops the worker and ends the run
// as cancelled. The error is non-nil only when the run couldn't start; how
// it ended is in the returned run's Work.
func (s *Service) Work(ctx context.Context, options WorkOptions, events func(Event)) (Run, error) {
	emit := s.recorder(events)
	if strings.TrimSpace(options.Task) == "" {
		return Run{}, errors.New("a task is required")
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = DefaultMaxAttempts
	}
	if options.Timeout <= 0 {
		options.Timeout = DefaultWorkTimeout
	}
	store, err := s.runRepository()
	if err != nil {
		return Run{}, err
	}
	published, err := store.GetVersion(options.ContractID, options.Version)
	if err != nil {
		return Run{}, err
	}
	root, err := filepath.Abs(options.ProjectDir)
	if err != nil {
		return Run{}, err
	}
	config := DefaultConfig()
	if s.LoadConfig != nil {
		if config, err = s.LoadConfig(root); err != nil {
			return Run{}, err
		}
	}
	worker, err := config.Worker(options.Worker)
	if err != nil {
		return Run{}, err
	}
	baseline := Baseline(root)
	if baseline == "" || baseline == EmptyTree {
		return Run{}, errors.New("assay work needs a git repository with at least one commit: it works on a copy of the last commit")
	}

	id := rand.Text()
	name := strings.ToLower(id)
	workspace := filepath.Join(root, ".assay", "workspaces", name)
	branch := "assay/" + name
	if err := os.MkdirAll(filepath.Dir(workspace), 0700); err != nil {
		return Run{}, err
	}
	if _, err := git(root, nil, "worktree", "add", "--quiet", "-b", branch, workspace, baseline); err != nil {
		return Run{}, fmt.Errorf("create the workspace: %v", err)
	}
	started := time.Now().UTC()
	run := Run{
		ID: id, ContractID: options.ContractID, Version: published.Version, ProjectDir: root, Baseline: baseline,
		Status: "ready", CreatedAt: started, Attempts: []Attempt{},
		Work: &Work{
			Task: options.Task, Worker: worker.Name, Workspace: workspace, Branch: branch,
			MaxAttempts: options.MaxAttempts, Timeout: options.Timeout.Milliseconds(), Outcome: WorkRunning,
		},
	}
	if err := store.CreateRun(run); err != nil {
		return Run{}, err
	}
	emit(Event{Type: EventRunStarted, Run: &run, Attempt: len(run.Attempts)})
	return s.workLoop(ctx, store, run, published.Contract, worker, config, nil, options.Timeout, emit), nil
}

// workLoop runs attempts until the run is accepted or a limit is reached,
// continuing after the run's existing attempts. previous is the last
// checked attempt, with its evidence content, for the repair prompt.
func (s *Service) workLoop(ctx context.Context, store RunRepository, run Run, contract Contract, worker WorkerSpec, config Config, previous *Attempt, timeout time.Duration, emit func(Event)) Run {
	deadline := time.Now().Add(timeout)
	finish := func(outcome WorkOutcome, reason string) Run {
		finished := time.Now().UTC()
		run.Work.Outcome, run.Work.Reason, run.Work.FinishedAt = outcome, reason, &finished
		if err := store.UpdateWork(run.ID, *run.Work); err != nil {
			run.Work.Outcome, run.Work.Reason = WorkError, fmt.Sprintf("%s, but recording that failed: %v", reason, err)
		} else if latest, err := store.GetRun(run.ID); err == nil {
			run = latest
		}
		emit(Event{Type: EventRunFinished, Run: &run, Outcome: run.Work.Outcome, Reason: run.Work.Reason})
		return run
	}

	for number := len(run.Attempts) + 1; number <= run.Work.MaxAttempts; number++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return finish(WorkTimedOut, fmt.Sprintf("the %s time limit was reached before attempt %d", timeout, number))
		}
		emit(Event{Type: EventAttemptStarted, Run: &run, Attempt: number})
		prompt := workPrompt(contract, run, run.Work.Task, number, previous)
		record, blobs := s.runWorker(ctx, worker, config, run, number, prompt, remaining, func(line string) {
			emit(Event{Type: EventWorkerOutput, Run: &run, Attempt: number, Line: line})
		})
		emit(Event{Type: EventWorkerFinished, Run: &run, Attempt: number, Worker: &record})

		var attempt Attempt
		if record.Cancelled {
			now := time.Now().UTC()
			attempt = Attempt{StartedAt: now, CompletedAt: now, Status: Cancelled, Results: []RuleResult{}}
		} else {
			var err error
			attempt, err = s.evaluate(run)
			if err != nil {
				return finish(WorkError, fmt.Sprintf("checking attempt %d failed: %v", number, err))
			}
		}
		if commit, err := commitWorkspace(run.Work.Workspace, run.ID, number); err != nil {
			record.Error = strings.TrimSpace(record.Error + " " + fmt.Sprintf("commit the workspace: %v", err))
		} else {
			record.Commit = commit
		}
		attempt.Worker = &record
		if attempt.Blobs == nil {
			attempt.Blobs = map[string][]byte{}
		}
		for key, data := range blobs {
			attempt.Blobs[key] = data
		}
		stored, err := s.appendAttempt(run.ID, attempt)
		if err != nil {
			return finish(WorkError, fmt.Sprintf("recording attempt %d failed: %v", number, err))
		}
		run.Attempts, run.Status = stored.Attempts, stored.Status
		latest := run.Attempts[len(run.Attempts)-1]
		latest.Blobs = attempt.Blobs // the repair prompt quotes evidence content
		emit(Event{Type: EventAttemptFinished, Run: &run, Attempt: number})

		switch {
		case record.Cancelled:
			return finish(WorkCancelled, fmt.Sprintf("cancelled during attempt %d", number))
		case latest.Status == Passed:
			return finish(WorkAccepted, fmt.Sprintf("every rule passed on attempt %d", number))
		case record.Error != "" && record.Duration == 0:
			return finish(WorkError, fmt.Sprintf("the worker couldn't run: %s", record.Error))
		case !record.TimedOut && record.ExitCode != 0:
			return finish(WorkError, fmt.Sprintf("worker %s exited with status %d on attempt %d", worker.Name, record.ExitCode, number))
		}
		previous = &latest
	}
	return finish(WorkRejected, fmt.Sprintf("rules still failed after %d attempts", run.Work.MaxAttempts))
}

// ResumeOptions continues a work run.
type ResumeOptions struct {
	RunID       string
	MaxAttempts int           // total attempts allowed, counting earlier ones; 0 keeps the run's limit
	Timeout     time.Duration // time allowed from now; 0 uses the run's limit
}

// ResumeWork continues a work run that wasn't accepted: after Ctrl-C, a
// time limit, a worker error, or, with a higher MaxAttempts, the attempt
// limit. It uses the same workspace and branch, recreating the workspace
// from the branch if it was cleaned up, and tells the worker what failed
// in the last checked attempt.
func (s *Service) ResumeWork(ctx context.Context, options ResumeOptions, events func(Event)) (Run, error) {
	emit := s.recorder(events)
	store, err := s.runRepository()
	if err != nil {
		return Run{}, err
	}
	run, err := store.GetRun(options.RunID)
	if err != nil {
		return Run{}, err
	}
	if run.Work == nil {
		return Run{}, fmt.Errorf("run %s wasn't made by \"assay work\", so there's nothing to resume", strings.ToLower(run.ID))
	}
	if run.Work.Outcome == WorkAccepted {
		return Run{}, fmt.Errorf("run %s was accepted; there's nothing to resume", strings.ToLower(run.ID))
	}
	if options.MaxAttempts > 0 {
		run.Work.MaxAttempts = options.MaxAttempts
	}
	if len(run.Attempts) >= run.Work.MaxAttempts {
		return Run{}, fmt.Errorf("run %s has used all %d of its attempts; allow more with a higher --max-attempts", strings.ToLower(run.ID), run.Work.MaxAttempts)
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = time.Duration(run.Work.Timeout) * time.Millisecond
	}
	if timeout <= 0 {
		timeout = DefaultWorkTimeout
	}
	published, err := store.GetVersion(run.ContractID, run.Version)
	if err != nil {
		return Run{}, err
	}
	config := DefaultConfig()
	if s.LoadConfig != nil {
		if config, err = s.LoadConfig(run.ProjectDir); err != nil {
			return Run{}, err
		}
	}
	worker, err := config.Worker(run.Work.Worker)
	if err != nil {
		return Run{}, err
	}
	if err := ensureWorkspace(run); err != nil {
		return Run{}, err
	}

	// The repair prompt describes the last attempt that was checked.
	var previous *Attempt
	for i := len(run.Attempts) - 1; i >= 0; i-- {
		if run.Attempts[i].Status == Cancelled {
			continue
		}
		attempt := run.Attempts[i]
		attempt.Blobs = map[string][]byte{}
		for _, result := range attempt.Results {
			for _, item := range result.Evidence {
				if item.Kind == CommandEvidence && item.Blob != "" {
					if data, err := store.GetBlob(item.Blob); err == nil {
						attempt.Blobs[item.Blob] = data
					}
				}
			}
		}
		previous = &attempt
		break
	}

	run.Work.Outcome, run.Work.Reason, run.Work.FinishedAt, run.Work.Timeout = WorkRunning, "", nil, timeout.Milliseconds()
	if err := store.UpdateWork(run.ID, *run.Work); err != nil {
		return Run{}, err
	}
	emit(Event{Type: EventRunStarted, Run: &run, Attempt: len(run.Attempts)})
	return s.workLoop(ctx, store, run, published.Contract, worker, config, previous, timeout, emit), nil
}

// ensureWorkspace recreates a work run's workspace from its branch if it
// was removed.
func ensureWorkspace(run Run) error {
	if info, err := os.Stat(run.Work.Workspace); err == nil && info.IsDir() {
		return nil
	}
	git(run.ProjectDir, nil, "worktree", "prune")
	if _, err := git(run.ProjectDir, nil, "worktree", "add", "--quiet", run.Work.Workspace, run.Work.Branch); err != nil {
		return fmt.Errorf("recreate the workspace from branch %s: %v", run.Work.Branch, err)
	}
	return nil
}

// WorkspaceStatus describes a work run's workspace.
type WorkspaceStatus struct {
	Run     Run
	Exists  bool
	Removed bool   // removed by CleanWorkspaces
	Skipped string // why it wasn't removed
}

// Workspaces lists work runs' workspaces, newest first.
func (s *Service) Workspaces() ([]WorkspaceStatus, error) {
	store, err := s.runRepository()
	if err != nil {
		return nil, err
	}
	runs, err := store.ListRuns()
	if err != nil {
		return nil, err
	}
	var list []WorkspaceStatus
	for _, run := range runs {
		if run.Work == nil {
			continue
		}
		info, err := os.Stat(run.Work.Workspace)
		list = append(list, WorkspaceStatus{Run: run, Exists: err == nil && info.IsDir()})
	}
	return list, nil
}

// CleanWorkspaces removes the workspaces of finished work runs, or of the
// one run named. Branches are kept: they hold every attempt's commit, and
// a cleaned run can still be resumed. A workspace with changes that aren't
// committed is skipped unless force is set.
func (s *Service) CleanWorkspaces(runID string, force bool) ([]WorkspaceStatus, error) {
	list, err := s.Workspaces()
	if err != nil {
		return nil, err
	}
	var result []WorkspaceStatus
	for _, status := range list {
		if runID != "" && status.Run.ID != runID {
			continue
		}
		if !status.Exists {
			continue
		}
		if status.Run.Work.Outcome == WorkRunning {
			status.Skipped = "it's still running"
			result = append(result, status)
			continue
		}
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		if _, err := git(status.Run.ProjectDir, nil, append(args, status.Run.Work.Workspace)...); err != nil {
			status.Skipped = err.Error()
			if strings.Contains(err.Error(), "modified or untracked") {
				status.Skipped = "it has changes that aren't committed; use --force to remove it anyway"
			}
		} else {
			status.Removed, status.Exists = true, false
		}
		result = append(result, status)
	}
	return result, nil
}

// runWorker runs the worker once in the workspace with prompt on stdin,
// streaming its output lines to onLine.
func (s *Service) runWorker(ctx context.Context, worker WorkerSpec, config Config, run Run, number int, prompt string, remaining time.Duration, onLine func(string)) (WorkerRun, map[string][]byte) {
	blobs := map[string][]byte{}
	record := WorkerRun{Worker: worker.Name, Argv: worker.Command.Run, Sandboxed: worker.Sandbox}
	record.Prompt = Evidence{Kind: "prompt"}
	record.Prompt.store([]byte(prompt), blobs)

	spec := worker.Command
	spec.Name = worker.Name
	spec.WritesProject = true
	spec.Dir = ""
	if spec.Timeout <= 0 {
		spec.Timeout = DefaultWorkerTimeout
	}
	if spec.Timeout > remaining {
		spec.Timeout = remaining
	}
	c := newCollector(run.Dir(), run.ProjectDir, run.Baseline, nil)
	defer c.cleanup()
	scratch, err := c.scratchDir("worker", worker.Name)
	if err != nil {
		record.Error = err.Error()
		return record, blobs
	}
	out := &output{hash: sha256.New()}
	lines := &lineWriter{onLine: onLine}
	writer := io.MultiWriter(out, lines)
	result, err := runProcess(process{
		spec: spec, sandbox: worker.Sandbox && config.Sandbox, project: run.Dir(), state: filepath.Join(run.ProjectDir, ".assay"),
		scratch: scratch, cache: filepath.Join(run.ProjectDir, ".assay", "worker-cache", worker.Name),
		stdin: []byte(prompt), stdout: writer, stderr: writer, ctx: ctx,
	})
	lines.flush()
	record.Duration, record.ExitCode, record.TimedOut, record.Cancelled, record.Sandboxed = result.duration, result.exitCode, result.timedOut, result.cancelled, result.sandboxed
	if err != nil {
		record.Error = err.Error()
	}
	record.Output = Evidence{Kind: "output", Size: out.size, SHA256: hex.EncodeToString(out.hash.Sum(nil))}
	kept := out.kept.Bytes()
	record.Output.Truncated = int64(len(kept)) < out.size
	record.Output.Blob = sha(kept)
	blobs[record.Output.Blob] = kept
	return record, blobs
}

// commitWorkspace commits every change in the workspace, returning the new
// commit, or "" when nothing changed. assay's own state is never
// committed. Hooks and signing are skipped: the commit records the
// worker's result, and must not run project code or ask for anything.
func commitWorkspace(workspace, runID string, number int) (string, error) {
	if _, err := git(workspace, nil, "add", "--all", "--", ".", ":(exclude).assay"); err != nil {
		return "", err
	}
	if _, err := git(workspace, nil, "diff", "--cached", "--quiet"); err == nil {
		return "", nil
	}
	args := []string{"-c", "commit.gpgsign=false"}
	if name, _ := git(workspace, nil, "config", "user.email"); strings.TrimSpace(string(name)) == "" {
		args = append(args, "-c", "user.name=assay", "-c", "user.email=assay@localhost")
	}
	message := fmt.Sprintf("assay: attempt %d of run %s", number, strings.ToLower(runID))
	if _, err := git(workspace, nil, append(args, "commit", "--quiet", "--no-verify", "-m", message)...); err != nil {
		return "", err
	}
	head, err := git(workspace, nil, "rev-parse", "HEAD")
	return strings.TrimSpace(string(head)), err
}

// lineWriter splits written bytes into lines for onLine.
type lineWriter struct {
	mu      sync.Mutex
	pending bytes.Buffer
	onLine  func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending.Write(p)
	for {
		line, err := w.pending.ReadString('\n')
		if err != nil {
			w.pending.WriteString(line) // incomplete; wait for the rest
			return len(p), nil
		}
		w.emit(strings.TrimRight(line, "\r\n"))
	}
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending.Len() > 0 {
		w.emit(w.pending.String())
		w.pending.Reset()
	}
}

func (w *lineWriter) emit(line string) {
	if w.onLine != nil {
		w.onLine(strings.ToValidUTF8(line, "?"))
	}
}

// maxRepairOutput bounds how much of a failed command's output the repair
// prompt quotes.
const maxRepairOutput = 4 << 10

// workPrompt is what the worker is told: the task, the rules its work is
// checked against, and, after a failed attempt, what failed and why.
func workPrompt(contract Contract, run Run, task string, number int, previous *Attempt) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are working in a git workspace: a copy of the project at commit %s. Make the changes the task asks for in this directory.\n", shortHash(run.Baseline))
	b.WriteString("When you exit, assay checks the workspace against the rules below. Only those checks decide whether the work is accepted; what you report about your own work is ignored.\n\n")
	fmt.Fprintf(&b, "Task:\n%s\n\n", strings.TrimSpace(task))
	fmt.Fprintf(&b, "Rules (contract %s@%d):\n", contract.ID, run.Version)
	for _, rule := range contract.Rules {
		fmt.Fprintf(&b, "- %s: %s\n  %s\n", rule.ID, rule.Criterion, describeCheck(rule))
		if len(rule.DependsOn) > 0 {
			fmt.Fprintf(&b, "  Checked only after these pass: %s\n", strings.Join(rule.DependsOn, ", "))
		}
	}
	if previous == nil {
		fmt.Fprintf(&b, "\nThis is attempt %d of %d.\n", number, run.Work.MaxAttempts)
		return b.String()
	}
	fmt.Fprintf(&b, "\nThis is attempt %d of %d. Your previous attempt's changes are still in the workspace. assay checked them, and these rules didn't pass:\n", number, run.Work.MaxAttempts)
	var passed []string
	for _, result := range previous.Results {
		if result.Status == Passed {
			passed = append(passed, result.RuleID)
			continue
		}
		reason := result.Reason
		if result.Status == Blocked {
			reason = "not checked: " + reason
		}
		fmt.Fprintf(&b, "\n- %s (%s): %s\n", result.RuleID, result.Status, reason)
		for _, item := range result.Evidence {
			if item.Kind != CommandEvidence || item.Error != "" || item.ExitCode == 0 && !item.TimedOut {
				continue
			}
			content := previous.Blobs[item.Blob]
			if len(content) > maxRepairOutput {
				content = append([]byte("…"), content[len(content)-maxRepairOutput:]...)
			}
			fmt.Fprintf(&b, "  Output of %s:\n%s\n", item.Command, indent(strings.TrimRight(strings.ToValidUTF8(string(content), "?"), "\n"), "    "))
		}
	}
	if len(passed) > 0 {
		fmt.Fprintf(&b, "\nThese rules passed; keep them passing: %s.\n", strings.Join(passed, ", "))
	}
	b.WriteString("\nFix what failed.\n")
	return b.String()
}

// describeCheck says how assay checks a rule, for the worker.
func describeCheck(rule Rule) string {
	switch rule.Kind {
	case FileExists:
		return fmt.Sprintf("Checked by: %s must exist as a file.", rule.Path)
	case CommandSucceeds:
		return fmt.Sprintf("Checked by: the approved command %s must exit with status 0.", rule.Command)
	case ReviewCheck:
		var seen []string
		for _, spec := range rule.Evidence {
			switch spec.Kind {
			case FileEvidence:
				seen = append(seen, "file "+spec.Path)
			case DiffEvidence:
				seen = append(seen, "the diff of your changes")
			case CommandEvidence:
				seen = append(seen, "the output of "+spec.Command)
			}
		}
		return "Checked by: a reviewer judges it from " + strings.Join(seen, ", ") + "."
	}
	return ""
}

func indent(text, prefix string) string {
	return prefix + strings.ReplaceAll(text, "\n", "\n"+prefix)
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// recorder returns a function that stores each event of a run, so another
// process can follow it, then passes it to events. A failure to store an
// event doesn't stop the run.
func (s *Service) recorder(events func(Event)) func(Event) {
	return func(e Event) {
		e.Time = time.Now().UTC()
		if e.Run != nil {
			if store, err := s.runRepository(); err == nil {
				if seq, err := store.AppendEvent(e.Run.ID, e); err == nil {
					e.Seq = seq
				}
			}
		}
		if events != nil {
			events(e)
		}
	}
}

// Events returns a work run's stored events after seq, in order, with
// Run unset.
func (s *Service) Events(runID string, after int) ([]Event, error) {
	store, err := s.runRepository()
	if err != nil {
		return nil, err
	}
	return store.ListEvents(runID, after)
}
