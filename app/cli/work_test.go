package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// workCLIProject is a committed git repository with a contract that needs
// README.md, and a worker that writes it.
func workCLIProject(t *testing.T, worker string) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "T")
	mustRun(t, dir, "init")
	os.WriteFile(filepath.Join(dir, "harness", "readme.yaml"), []byte("version: 1\nid: readme\nname: README\nrules:\n  - {id: readme, criterion: Project has a README, check: file_exists, path: README.md}\n"), 0644)
	git("add", "-A")
	git("commit", "-q", "-m", "start")
	os.WriteFile(filepath.Join(dir, ".harness", "config.yaml"), []byte("version: 1\nsandbox: off\nworkers:\n  writer:\n    run: [sh, -c, '"+worker+"']\n"), 0600)
	mustRun(t, dir, "contract", "publish", "harness/readme.yaml", "--yes")
	return dir
}

func TestWorkCommand(t *testing.T) {
	dir := workCLIProject(t, `cat >/dev/null; echo writing; echo hello > README.md`)
	stdout, stderr, code := runIn(dir, false, "work", "readme", "--task", "Write a README")
	if code != ExitOK {
		t.Fatalf("work: exit %d\n%s%s", code, stdout, stderr)
	}
	for _, want := range []string{"work readme@1 · run ", "worker writer", "attempt 1 of 3 · running writer", "  │ writing\n", "writer exited 0 after ", "unsandboxed", "committed the workspace as ", "✓ readme"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("progress lacks %q:\n%s", want, stderr)
		}
	}
	if !strings.HasPrefix(stdout, "ACCEPTED · readme@1 · run ") || !strings.Contains(stdout, "every rule passed on attempt 1") || !strings.Contains(stdout, "harness never merges") {
		t.Fatalf("result:\n%s", stdout)
	}

	show := mustRun(t, dir, "show")
	for _, want := range []string{"work by writer · accepted: every rule passed on attempt 1", "task: Write a README", "  writer exited 0 after "} {
		if !strings.Contains(show, want) {
			t.Errorf("show lacks %q:\n%s", want, show)
		}
	}
	if prompt := mustRun(t, dir, "evidence", "--prompt"); !strings.Contains(prompt, "Task:\nWrite a README") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	if out := mustRun(t, dir, "evidence", "--output"); out != "writing\n" {
		t.Fatalf("output: %q", out)
	}
	var shown struct {
		Run runJSON `json:"run"`
	}
	json.Unmarshal([]byte(mustRun(t, dir, "show", "--json")), &shown)
	if w := shown.Run.Work; w == nil || w.Outcome != "accepted" || w.Worker != "writer" || shown.Run.Attempts[0].Worker == nil || shown.Run.Attempts[0].Worker.Commit == "" {
		t.Fatalf("json: %+v", shown.Run)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
		t.Fatal("the worker wrote to the project, not the workspace")
	}
}

func TestWorkCommandRejectsAndUsage(t *testing.T) {
	dir := workCLIProject(t, `cat >/dev/null; echo nothing to do`)
	stdout, _, code := runIn(dir, false, "work", "readme", "--task", "x", "--max-attempts", "2", "--quiet")
	if code != ExitNegative || !strings.HasPrefix(stdout, "REJECTED · readme@1") || !strings.Contains(stdout, "rules still failed after 2 attempts") {
		t.Fatalf("rejected: exit %d\n%s", code, stdout)
	}
	if _, stderr, code := runIn(dir, false, "work", "readme"); code != ExitUsage || !strings.Contains(stderr, "--task") {
		t.Fatalf("no task: exit %d\n%s", code, stderr)
	}
	if _, stderr, code := runIn(dir, false, "work", "readme", "--task", "x", "--worker", "nobody"); code != ExitOperational || !strings.Contains(stderr, `worker "nobody" isn't configured`) {
		t.Fatalf("unknown worker: exit %d\n%s", code, stderr)
	}
	if _, stderr, code := runIn(dir, false, "evidence", "--prompt", "--output"); code != ExitUsage || !strings.Contains(stderr, "not both") {
		t.Fatalf("both: exit %d\n%s", code, stderr)
	}
}
