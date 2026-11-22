package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Work over MCP. start_work runs "assay work" in the background and
// returns as soon as the run starts; the agent follows it with get_run and
// can stop it with cancel_work. When the client goes away, the server
// cancels the work it started, so no run is left marked as running.

// maxMCPWork is how many work runs one server runs at once.
const maxMCPWork = 3

// mcpStopWait is how long the server waits for cancelled runs to record
// that they ended before it exits.
const mcpStopWait = 30 * time.Second

// mcpWorkWait caps how long get_run waits for a running run to change.
const mcpWorkWait = 120 * time.Second

type mcpWork struct {
	mu   sync.Mutex
	runs map[string]*mcpRunning // by run ID, while running
	all  sync.WaitGroup
}

type mcpRunning struct {
	id       string
	cancel   context.CancelFunc
	done     chan struct{} // closed when the command has returned
	finished bool
}

func (w *mcpWork) running() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	ids := make([]string, 0, len(w.runs))
	for id := range w.runs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// stopAll cancels every run and waits, up to wait, for them to end.
func (w *mcpWork) stopAll(wait time.Duration) {
	w.mu.Lock()
	for _, r := range w.runs {
		r.cancel()
	}
	w.mu.Unlock()
	done := make(chan struct{})
	go func() { w.all.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
	}
}

type (
	startWorkInput struct {
		Contract    string `json:"contract,omitempty" jsonschema:"a published contract or suite: its ID for the latest version, or id@version"`
		Task        string `json:"task,omitempty" jsonschema:"what the worker should do"`
		Worker      string `json:"worker,omitempty" jsonschema:"a worker configured in .assay/config.yaml; the default worker when empty"`
		MaxAttempts int    `json:"max_attempts,omitempty" jsonschema:"stop after this many attempts (default 3); with resume, counting the run's earlier attempts"`
		Timeout     string `json:"timeout,omitempty" jsonschema:"stop after this long in all, like 30m or 1h (default 30m); with resume, from now"`
		Resume      string `json:"resume,omitempty" jsonschema:"continue this run (that wasn't accepted) instead of starting one; its contract, task, and worker are kept"`
	}
	cancelWorkInput struct {
		Run string `json:"run" jsonschema:"the run ID, or any unique prefix, of work this server started"`
	}
)

func addWorkTools(server *mcp.Server, t mcpTool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "start_work",
		Description: "Have a worker do a task against a published contract or suite, as \"assay work\" does: in a new git worktree on branch assay/<run>, checking each attempt and sending back what failed, until every rule passes or a limit is reached. Returns the run ID as soon as the run starts; follow it with get_run (wait_seconds waits for progress) and stop it with cancel_work. Nothing is merged.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(true)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in startWorkInput) (*mcp.CallToolResult, any, error) {
		return t.startWork(ctx, in), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cancel_work",
		Description: "Cancel a work run this server started. The worker is stopped and the run ends as cancelled; it can be continued later with start_work and resume. Returns the run.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cancelWorkInput) (*mcp.CallToolResult, any, error) {
		return t.cancelWork(ctx, in.Run), nil, nil
	})
}

func (t mcpTool) startWork(ctx context.Context, in startWorkInput) *mcp.CallToolResult {
	args := []string{"work", "--json", "--quiet"}
	if in.Resume != "" {
		if in.Contract != "" || in.Task != "" || in.Worker != "" {
			return toolError(errors.New("resume continues a run with its own contract, task, and worker; give only the run and limits"))
		}
		args = append(args, "--resume", in.Resume)
	} else {
		if err := errors.Join(required("contract", in.Contract), required("task", in.Task)); err != nil {
			return toolError(err)
		}
		args = append(args, "--task", in.Task)
		if in.Worker != "" {
			args = append(args, "--worker", in.Worker)
		}
	}
	if in.MaxAttempts != 0 {
		args = append(args, "--max-attempts", strconv.Itoa(in.MaxAttempts))
	}
	if in.Timeout != "" {
		args = append(args, "--timeout", in.Timeout)
	}
	if in.Resume == "" {
		args = append(args, "--", in.Contract)
	}

	w := t.work
	if running := w.running(); len(running) >= maxMCPWork {
		return toolError(fmt.Errorf("%d work runs are already going (%s); wait for one to end or cancel one", len(running), strings.Join(shortIDs(running), ", ")))
	}
	// The run outlives this call, so it gets its own context.
	runCtx, cancel := context.WithCancel(context.Background())
	running := &mcpRunning{cancel: cancel, done: make(chan struct{})}
	started := make(chan string, 1)
	finished := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	env := Env{Stdout: &stdout, Stderr: &stderr, Dir: t.root, Context: runCtx, WorkStarted: func(id string) {
		select {
		case started <- id:
		default:
		}
	}}
	w.all.Add(1)
	go func() {
		defer w.all.Done()
		code := Run(args, env)
		w.mu.Lock()
		running.finished = true
		if running.id != "" {
			delete(w.runs, running.id)
		}
		w.mu.Unlock()
		cancel()
		close(running.done)
		finished <- code
	}()

	select {
	case id := <-started:
		w.mu.Lock()
		running.id = id
		if !running.finished {
			w.runs[id] = running
		}
		w.mu.Unlock()
		reply, _ := json.MarshalIndent(struct {
			Schema  int    `json:"schema"`
			Run     string `json:"run"`
			ShortID string `json:"shortId"`
			Status  string `json:"status"`
			Next    string `json:"next"`
		}{JSONSchema, strings.ToLower(id), shortID(id), "running", "Follow it with get_run (set wait_seconds to wait for the next attempt or the end); cancel_work stops it."}, "", "  ")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(reply)}}}
	case code := <-finished:
		// It ended before starting a run: a usage error, an unknown
		// contract or worker, or a project that isn't a git repository.
		return commandResult(code, &stdout, &stderr)
	case <-ctx.Done():
		cancel()
		return toolError(ctx.Err())
	}
}

func (t mcpTool) cancelWork(ctx context.Context, prefix string) *mcp.CallToolResult {
	if err := required("run", prefix); err != nil {
		return toolError(err)
	}
	w := t.work
	w.mu.Lock()
	var matches []*mcpRunning
	for id, r := range w.runs {
		if strings.HasPrefix(strings.ToUpper(id), strings.ToUpper(prefix)) {
			matches = append(matches, r)
		}
	}
	w.mu.Unlock()
	switch len(matches) {
	case 0:
		return toolError(fmt.Errorf("no work started by this server is running as %q; work started elsewhere is stopped where it was started", prefix))
	case 1:
	default:
		return toolError(fmt.Errorf("%q matches %d running work runs; use more characters", prefix, len(matches)))
	}
	r := matches[0]
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(mcpStopWait):
		return toolError(fmt.Errorf("run %s didn't stop within %s", shortID(r.id), mcpStopWait))
	case <-ctx.Done():
		return toolError(ctx.Err())
	}
	return t.run(ctx, "show", "--json", "--", r.id)
}

// waitForRun waits, up to wait, while a work run is running and unchanged,
// so an agent can follow it without polling quickly. It returns the run
// as "show --json" prints it.
func (t mcpTool) waitForRun(ctx context.Context, args []string, wait time.Duration) *mcp.CallToolResult {
	show := func() (int, *bytes.Buffer, *bytes.Buffer) {
		var stdout, stderr bytes.Buffer
		code := Run(args, Env{Stdout: &stdout, Stderr: &stderr, Dir: t.root, Context: ctx})
		return code, &stdout, &stderr
	}
	code, stdout, stderr := show()
	first := stdout.String()
	deadline := time.Now().Add(min(wait, mcpWorkWait))
	for code == ExitOK && workRunning(first) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return toolError(ctx.Err())
		case <-time.After(mcpPoll):
		}
		code, stdout, stderr = show()
		if stdout.String() != first {
			break
		}
	}
	return commandResult(code, stdout, stderr)
}

// mcpPoll is how often waitForRun looks for a change.
var mcpPoll = 500 * time.Millisecond

func workRunning(showJSON string) bool {
	var out struct {
		Run struct {
			Work *struct {
				Outcome string `json:"outcome"`
			} `json:"work"`
		} `json:"run"`
	}
	return json.Unmarshal([]byte(showJSON), &out) == nil && out.Run.Work != nil && out.Run.Work.Outcome == "running"
}

func shortIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = shortID(id)
	}
	return out
}
