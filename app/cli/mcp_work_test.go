package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startedRun reads the run ID start_work returned.
func startedRun(t *testing.T, text string) string {
	t.Helper()
	var out struct {
		Run    string `json:"run"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil || out.Run == "" || out.Status != "running" {
		t.Fatalf("start_work: %v\n%s", err, text)
	}
	return out.Run
}

// waitUntilEnded follows a work run with get_run until it ends.
func waitUntilEnded(t *testing.T, s *mcp.ClientSession, run string) runJSON {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		text, isErr := call(t, s, "get_run", map[string]any{"run": run, "wait_seconds": 5})
		var out struct {
			Run runJSON `json:"run"`
		}
		firstJSON(t, text, &out)
		if isErr || out.Run.Work == nil {
			t.Fatalf("get_run: %s", text)
		}
		if out.Run.Work.Outcome != "running" {
			return out.Run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never ended: %s", text)
		}
	}
}

// TestMCPWorkMilestone is the plan's milestone: an outside agent requests
// a task through MCP, assay rejects a flawed attempt, the worker retries,
// assay accepts, and the evidence behind the decision is recorded.
func TestMCPWorkMilestone(t *testing.T) {
	// The worker forgets the README the first time.
	dir := workCLIProject(t, `cat >/dev/null; if [ -f .tried ]; then echo hello > README.md; else touch .tried; echo "first try"; fi`)
	s := mcpClient(t, dir)

	text, isErr := call(t, s, "start_work", map[string]any{"contract": "readme", "task": "Write a README", "max_attempts": 3})
	if isErr {
		t.Fatalf("start_work: %s", text)
	}
	run := startedRun(t, text)
	final := waitUntilEnded(t, s, run)
	if final.Work.Outcome != "accepted" || len(final.Attempts) != 2 || final.Attempts[0].Status != "failed" || final.Attempts[1].Status != "passed" {
		t.Fatalf("run: %+v", final)
	}
	if final.Attempts[0].Results[0].Reason != "README.md does not exist" {
		t.Errorf("attempt 1: %+v", final.Attempts[0].Results)
	}

	// The second prompt told the worker what failed in the first.
	stdout, _, code := runIn(dir, false, "evidence", run, "--prompt", "--attempt", "2")
	if code != ExitOK || !strings.Contains(stdout, "README.md does not exist") {
		t.Fatalf("attempt 2's prompt: exit %d\n%s", code, stdout)
	}
	// Agents read it over MCP too.
	text = mustCall(t, s, "get_evidence", map[string]any{"run": run, "worker": "prompt", "attempt": 2})
	if !strings.Contains(text, "README.md does not exist") {
		t.Fatalf("get_evidence worker prompt:\n%s", text)
	}
	if text = mustCall(t, s, "get_evidence", map[string]any{"run": run, "worker": "output", "attempt": 1}); !strings.Contains(text, "first try") {
		t.Fatalf("get_evidence worker output:\n%s", text)
	}
	if text, isErr = call(t, s, "get_evidence", map[string]any{"worker": "log"}); !isErr {
		t.Fatalf("bad worker value: %s", text)
	}
	show := mustRun(t, dir, "show", run, "--evidence")
	for _, want := range []string{"work by writer · accepted", "attempt 1 · failed", "attempt 2 · passed"} {
		if !strings.Contains(show, want) {
			t.Errorf("show --evidence lacks %q:\n%s", want, show)
		}
	}
}

func TestMCPCancelWork(t *testing.T) {
	dir := workCLIProject(t, `cat >/dev/null; echo started; sleep 30`)
	s := mcpClient(t, dir)
	run := startedRun(t, mustCall(t, s, "start_work", map[string]any{"contract": "readme", "task": "Write a README"}))

	// get_run waits while nothing changes, then answers.
	began := time.Now()
	text, _ := call(t, s, "get_run", map[string]any{"run": run, "wait_seconds": 1})
	if time.Since(began) < 900*time.Millisecond || !strings.Contains(text, `"outcome": "running"`) {
		t.Fatalf("wait: %s after %s", text, time.Since(began))
	}

	text, isErr := call(t, s, "cancel_work", map[string]any{"run": run[:6]})
	if isErr || !strings.Contains(text, `"outcome": "cancelled"`) {
		t.Fatalf("cancel_work: %s", text)
	}
	if text, isErr = call(t, s, "cancel_work", map[string]any{"run": run}); !isErr || !strings.Contains(text, "no work started by this server") {
		t.Fatalf("cancelling again: %s", text)
	}
	// Work that can't start says why.
	if text, isErr = call(t, s, "start_work", map[string]any{"contract": "nope", "task": "x"}); !isErr || !strings.Contains(text, `no published contract "nope"`) {
		t.Fatalf("unknown contract: %s", text)
	}
	if text, isErr = call(t, s, "start_work", map[string]any{"contract": "readme"}); !isErr || !strings.Contains(text, "task is required") {
		t.Fatalf("no task: %s", text)
	}
	if text, isErr = call(t, s, "start_work", map[string]any{"resume": run, "task": "x"}); !isErr || !strings.Contains(text, "give only the run and limits") {
		t.Fatalf("resume with a task: %s", text)
	}

	// Resuming continues the run; it's cancelled again when the client
	// goes away.
	run2 := startedRun(t, mustCall(t, s, "start_work", map[string]any{"resume": run}))
	if run2 != run {
		t.Fatalf("resume started %s, not %s", run2, run)
	}
}

func TestMCPDisconnectCancelsWork(t *testing.T) {
	dir := workCLIProject(t, `cat >/dev/null; sleep 30`)
	ctx := context.Background()
	server, work := newMCPServer(dir)
	serverSide, clientSide := mcp.NewInMemoryTransports()
	session, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := startedRun(t, mustCall(t, client, "start_work", map[string]any{"contract": "readme", "task": "Write a README"}))
	client.Close()
	session.Wait()
	work.stopAll(15 * time.Second) // what runMCP does when the client goes away

	var out struct {
		Run runJSON `json:"run"`
	}
	stdout, _, _ := runIn(dir, false, "show", run, "--json")
	json.Unmarshal([]byte(stdout), &out)
	if out.Run.Work == nil || out.Run.Work.Outcome != "cancelled" {
		t.Fatalf("after disconnecting: %s", stdout)
	}
}

func mustCall(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	text, isErr := call(t, s, tool, args)
	if isErr {
		t.Fatalf("%s: %s", tool, text)
	}
	return text
}
