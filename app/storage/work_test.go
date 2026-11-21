package storage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/backend"
)

// workProject is a git repository with one commit and a contract that needs
// README.md and an approved command that checks done.txt says "done".
func workProject(t *testing.T) (string, *backend.Service) {
	t.Helper()
	dir := gitRepo(t)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/.harness/\n"), 0644)
	os.WriteFile(filepath.Join(dir, "main.txt"), []byte("start\n"), 0644)
	commitAll(t, dir)
	store, s := openTest(t)
	publish(t, s, backend.Contract{ID: "task", Name: "Task", Rules: []backend.Rule{
		{ID: "readme", Criterion: "Project has a README", Kind: backend.FileExists, Path: "README.md"},
		{ID: "done", Criterion: "done.txt says done", Kind: backend.CommandSucceeds, Command: "check-done", DependsOn: []string{"readme"}},
	}})
	runStores["work:"+dir] = store
	return dir, s
}

func workConfig(sandbox bool, workers map[string]string) backend.Config {
	config := backend.DefaultConfig()
	config.Sandbox = sandbox
	config.Commands["check-done"] = backend.CommandSpec{Name: "check-done", Run: sh(`grep -qx done done.txt || { echo "done.txt doesn't say done"; exit 1; }`)}
	for name, script := range workers {
		config.Workers[name] = backend.WorkerSpec{Name: name, Command: backend.CommandSpec{Run: sh(script), Timeout: 30 * time.Second}, Sandbox: sandbox}
	}
	return config
}

func work(t *testing.T, dir string, s *backend.Service, config backend.Config, worker string, maxAttempts int, timeout time.Duration) (backend.Run, []backend.Event) {
	t.Helper()
	s.LoadConfig = func(string) (backend.Config, error) { return config, nil }
	var mu sync.Mutex
	var events []backend.Event
	run, err := s.Work(context.Background(), backend.WorkOptions{
		ContractID: "task", Version: 1, ProjectDir: dir, Task: "Add a README and mark it done", Worker: worker, MaxAttempts: maxAttempts, Timeout: timeout,
	}, func(e backend.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})
	if err != nil {
		t.Fatal(err)
	}
	runStores[run.ID] = runStores["work:"+dir]
	return run, events
}

// fixesOnRepair writes a README at once, and done.txt only once it's told
// what failed.
const fixesOnRepair = `prompt=$(cat); echo "# Project" > README.md; echo "working"
if printf %s "$prompt" | grep -q "doesn't say done"; then echo done > done.txt; echo "fixed it"; fi
echo "all done, everything passes"`

func TestWorkAcceptsAfterRepair(t *testing.T) {
	dir, s := workProject(t)
	run, events := work(t, dir, s, workConfig(false, map[string]string{"fixer": fixesOnRepair}), "", 3, time.Minute)

	w := run.Work
	if w.Outcome != backend.WorkAccepted || w.Reason != "every rule passed on attempt 2" || len(run.Attempts) != 2 || run.Status != string(backend.Passed) {
		t.Fatalf("work: %+v, %d attempts, status %s", w, len(run.Attempts), run.Status)
	}
	first, second := run.Attempts[0], run.Attempts[1]
	if first.Status != backend.Failed || second.Status != backend.Passed {
		t.Fatalf("attempt statuses: %s, %s", first.Status, second.Status)
	}
	// The repair prompt carries the failure and the command's output.
	prompt := string(mustBlob(t, run, second.Worker.Prompt))
	for _, want := range []string{"Task:\nAdd a README and mark it done", "- done: done.txt says done", "This is attempt 2 of 3", "- done (failed): check-done exited with status 1", "doesn't say done", "keep them passing: readme"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("repair prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(string(mustBlob(t, run, first.Worker.Prompt)), "previous attempt") {
		t.Error("the first prompt mentions a previous attempt")
	}
	if out := string(mustBlob(t, run, second.Worker.Output)); !strings.Contains(out, "fixed it") {
		t.Errorf("worker output: %q", out)
	}

	// Each attempt is a commit on the run's branch; the project is untouched.
	if first.Worker.Commit == "" || second.Worker.Commit == "" {
		t.Fatalf("commits: %q %q", first.Worker.Commit, second.Worker.Commit)
	}
	log, _ := exec.Command("git", "-C", dir, "log", "--format=%s", w.Branch).Output()
	if !strings.HasPrefix(string(log), "harness: attempt 2 of run "+strings.ToLower(run.ID)+"\nharness: attempt 1 of run") {
		t.Fatalf("branch log:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
		t.Fatal("the worker changed the project instead of the workspace")
	}
	if status, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); len(status) != 0 {
		t.Fatalf("the project's working tree changed:\n%s", status)
	}
	if content, _ := os.ReadFile(filepath.Join(w.Workspace, "done.txt")); string(content) != "done\n" {
		t.Fatalf("workspace done.txt: %q", content)
	}

	var types []string
	for _, e := range events {
		if e.Type != backend.EventWorkerOutput {
			types = append(types, string(e.Type))
		}
	}
	if got := strings.Join(types, " "); got != "run_started attempt_started worker_finished attempt_finished attempt_started worker_finished attempt_finished run_finished" {
		t.Fatalf("events: %s", got)
	}
}

func TestWorkLimits(t *testing.T) {
	dir, s := workProject(t)
	config := workConfig(false, map[string]string{
		"never":   `cat >/dev/null; echo "# Project" > README.md`,
		"crashes": `cat >/dev/null; echo "out of credits" >&2; exit 3`,
		"slow":    `cat >/dev/null; sleep 30`,
	})

	run, _ := work(t, dir, s, config, "never", 2, time.Minute)
	if run.Work.Outcome != backend.WorkRejected || run.Work.Reason != "rules still failed after 2 attempts" || len(run.Attempts) != 2 {
		t.Fatalf("never fixes: %+v, %d attempts", run.Work, len(run.Attempts))
	}

	run, _ = work(t, dir, s, config, "crashes", 3, time.Minute)
	if run.Work.Outcome != backend.WorkError || run.Work.Reason != "worker crashes exited with status 3 on attempt 1" || len(run.Attempts) != 1 {
		t.Fatalf("crashes: %+v", run.Work)
	}
	if out := string(mustBlob(t, run, run.Attempts[0].Worker.Output)); !strings.Contains(out, "out of credits") {
		t.Fatalf("crash output: %q", out)
	}

	started := time.Now()
	run, _ = work(t, dir, s, config, "slow", 3, 2*time.Second)
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("the run's time limit didn't stop the worker (%s)", elapsed)
	}
	if run.Work.Outcome != backend.WorkTimedOut || len(run.Attempts) != 1 || !run.Attempts[0].Worker.TimedOut {
		t.Fatalf("slow: %+v, %d attempts", run.Work, len(run.Attempts))
	}
}

func TestWorkCancel(t *testing.T) {
	dir, s := workProject(t)
	config := workConfig(false, map[string]string{"slow": `cat >/dev/null; echo started; sleep 30`})
	s.LoadConfig = func(string) (backend.Config, error) { return config, nil }
	ctx, cancel := context.WithCancel(context.Background())
	started := time.Now()
	run, err := s.Work(ctx, backend.WorkOptions{ContractID: "task", Version: 1, ProjectDir: dir, Task: "x"}, func(e backend.Event) {
		if e.Type == backend.EventWorkerOutput {
			cancel()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("cancelling didn't stop the worker (%s)", elapsed)
	}
	if run.Work.Outcome != backend.WorkCancelled || len(run.Attempts) != 1 || run.Attempts[0].Status != backend.Cancelled || !run.Attempts[0].Worker.Cancelled {
		t.Fatalf("cancelled: %+v %+v", run.Work, run.Attempts)
	}
	if _, err := os.Stat(run.Work.Workspace); err != nil {
		t.Fatalf("the workspace wasn't kept: %v", err)
	}
}

func TestWorkNeedsARepositoryAndAWorker(t *testing.T) {
	_, s := openTest(t)
	publish(t, s, backend.Contract{ID: "task", Name: "Task", Rules: []backend.Rule{{ID: "r", Criterion: "R", Kind: backend.FileExists, Path: "R"}}})
	s.LoadConfig = func(string) (backend.Config, error) { return workConfig(false, map[string]string{"w": "true"}), nil }
	options := backend.WorkOptions{ContractID: "task", Version: 1, ProjectDir: t.TempDir(), Task: "x"}
	if _, err := s.Work(context.Background(), options, nil); err == nil || !strings.Contains(err.Error(), "needs a git repository with at least one commit") {
		t.Fatalf("no repository: %v", err)
	}
	options.ProjectDir, options.Worker = gitRepo(t), "nobody"
	if _, err := s.Work(context.Background(), options, nil); err == nil || !strings.Contains(err.Error(), `worker "nobody" isn't configured`) {
		t.Fatalf("unknown worker: %v", err)
	}
	options.Task = "  "
	if _, err := s.Work(context.Background(), options, nil); err == nil || !strings.Contains(err.Error(), "a task is required") {
		t.Fatalf("empty task: %v", err)
	}
}

// A sandboxed worker can write in its workspace, but not to the project,
// harness's state, or anywhere else.
func TestSandboxLimitsWorkers(t *testing.T) {
	requireSandbox(t)
	dir, s := workProject(t)
	home := t.TempDir()
	script := `cat >/dev/null
echo "# Project" > README.md && echo wrote-workspace
echo x > "` + dir + `/escaped.txt" 2>/dev/null && echo wrote-project
echo x > "` + dir + `/.harness/escaped.txt" 2>/dev/null && echo wrote-state
echo x > "` + home + `/escaped.txt" 2>/dev/null && echo wrote-elsewhere
echo done > done.txt
true`
	run, _ := work(t, dir, s, workConfig(true, map[string]string{"boxed": script}), "boxed", 1, time.Minute)
	out := string(mustBlob(t, run, run.Attempts[0].Worker.Output))
	if !strings.Contains(out, "wrote-workspace") || strings.Contains(out, "wrote-project") || strings.Contains(out, "wrote-state") || strings.Contains(out, "wrote-elsewhere") {
		t.Fatalf("worker output:\n%s", out)
	}
	if run.Work.Outcome != backend.WorkAccepted || !run.Attempts[0].Worker.Sandboxed {
		t.Fatalf("work: %+v", run.Work)
	}
	for _, path := range []string{filepath.Join(dir, "escaped.txt"), filepath.Join(dir, ".harness", "escaped.txt"), filepath.Join(home, "escaped.txt")} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("the worker wrote %s", path)
		}
	}
}
