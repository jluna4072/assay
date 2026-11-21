package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"assay/storage"
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
	os.WriteFile(filepath.Join(dir, "assay", "readme.yaml"), []byte("version: 1\nid: readme\nname: README\nrules:\n  - {id: readme, criterion: Project has a README, check: file_exists, path: README.md}\n"), 0644)
	git("add", "-A")
	git("commit", "-q", "-m", "start")
	os.WriteFile(filepath.Join(dir, ".assay", "config.yaml"), []byte("version: 1\nsandbox: off\nworkers:\n  writer:\n    run: [sh, -c, '"+worker+"']\n"), 0600)
	mustRun(t, dir, "contract", "publish", "assay/readme.yaml", "--yes")
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
	if !strings.HasPrefix(stdout, "ACCEPTED · readme@1 · run ") || !strings.Contains(stdout, "every rule passed on attempt 1") || !strings.Contains(stdout, "assay never merges") {
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

func TestWorkResumeAndWorkspaces(t *testing.T) {
	dir := workCLIProject(t, `cat >/dev/null; echo nothing to do`)
	stdout, _, code := runIn(dir, false, "work", "readme", "--task", "x", "--max-attempts", "1", "--quiet")
	if code != ExitNegative || !strings.Contains(stdout, "assay work --resume ") || !strings.Contains(stdout, "--max-attempts 3") {
		t.Fatalf("rejected: exit %d\n%s", code, stdout)
	}
	if _, stderr, code := runIn(dir, false, "work", "--resume", "zz"); code != ExitUsage || !strings.Contains(stderr, "no run matches") {
		t.Fatalf("unknown run: exit %d\n%s", code, stderr)
	}
	if _, stderr, code := runIn(dir, false, "work", "readme", "--resume", "x"); code != ExitUsage || !strings.Contains(stderr, "give only the run and limits") {
		t.Fatalf("resume with a contract: exit %d\n%s", code, stderr)
	}
	if _, stderr, code := runIn(dir, false, "work", "--resume", ""); code == ExitOK {
		t.Fatalf("empty resume: %s", stderr)
	}
	if _, stderr, code := runIn(dir, false, "work", "--resume", firstRun(t, dir)); code != ExitUsage || !strings.Contains(stderr, "used all 1 of its attempts") {
		t.Fatalf("no attempts left: exit %d\n%s", code, stderr)
	}

	// The worker now writes the README; resuming with one more attempt passes.
	config := filepath.Join(dir, ".assay", "config.yaml")
	os.WriteFile(config, []byte("version: 1\nsandbox: off\nworkers:\n  writer:\n    run: [sh, -c, 'cat >/dev/null; echo hi > README.md']\n"), 0600)
	stdout, stderr, code := runIn(dir, false, "work", "--resume", firstRun(t, dir), "--max-attempts", "2")
	if code != ExitOK || !strings.Contains(stderr, "resuming after attempt 1 · readme@1") || !strings.Contains(stderr, "attempt 2 of 2") || !strings.HasPrefix(stdout, "ACCEPTED") {
		t.Fatalf("resume: exit %d\n%s%s", code, stdout, stderr)
	}

	list := mustRun(t, dir, "workspace", "list")
	if !strings.Contains(list, "accepted") || !strings.Contains(list, ".assay/workspaces/") {
		t.Fatalf("list:\n%s", list)
	}
	if out := mustRun(t, dir, "workspace", "clean"); !strings.HasPrefix(out, "removed  .assay/workspaces/") || !strings.Contains(out, "kept") {
		t.Fatalf("clean:\n%s", out)
	}
	if list := mustRun(t, dir, "workspace", "list"); !strings.Contains(list, "(removed)") {
		t.Fatalf("list after clean:\n%s", list)
	}
	if out := mustRun(t, dir, "workspace", "clean"); out != "no workspaces to remove\n" {
		t.Fatalf("clean again: %q", out)
	}
}

func firstRun(t *testing.T, dir string) string {
	t.Helper()
	var listed struct {
		Runs []struct{ ID string } `json:"runs"`
	}
	json.Unmarshal([]byte(mustRun(t, dir, "runs", "--json")), &listed)
	return listed.Runs[len(listed.Runs)-1].ID
}

// show --follow prints a run another caller is making, as it happens.
func TestShowFollow(t *testing.T) {
	dir := workCLIProject(t, `cat >/dev/null; echo step one; sleep 1; echo step two; echo hi > README.md`)
	done := make(chan int)
	go func() {
		_, _, code := runIn(dir, false, "work", "readme", "--task", "x")
		done <- code
	}()
	// Wait for the run to exist, then follow it.
	var run string
	for i := 0; i < 100 && run == ""; i++ {
		var listed struct {
			Runs []struct{ ID string } `json:"runs"`
		}
		if stdout, _, code := runIn(dir, false, "runs", "--json"); code == ExitOK {
			json.Unmarshal([]byte(stdout), &listed)
			if len(listed.Runs) > 0 {
				run = listed.Runs[0].ID
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	stdout, stderr, code := runIn(dir, false, "show", "--follow", run)
	if code != ExitOK {
		t.Fatalf("follow: exit %d\n%s%s", code, stdout, stderr)
	}
	for _, want := range []string{"work readme@1 · run ", "attempt 1 of 3 · running writer", "  │ step one\n  │ step two\n", "✓ readme", "ACCEPTED · readme@1"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("follow lacks %q:\n%s", want, stdout)
		}
	}
	if code := <-done; code != ExitOK {
		t.Fatalf("work: exit %d", code)
	}

	// Following a finished run replays it; JSON is one event per line.
	stdout = mustRun(t, dir, "show", "--follow", "--json", run)
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var first, last struct {
		Schema  int    `json:"schema"`
		Run     string `json:"run"`
		Seq     int    `json:"seq"`
		Type    string `json:"type"`
		Outcome string `json:"outcome"`
	}
	json.Unmarshal([]byte(lines[0]), &first)
	json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if first.Schema != 1 || first.Run != run || first.Seq != 1 || first.Type != "run_started" || last.Type != "run_finished" || last.Outcome != "accepted" {
		t.Fatalf("json lines:\n%s", stdout)
	}
	if out := mustRun(t, dir, "show", "--follow", "--quiet", run); strings.Contains(out, "│") {
		t.Fatalf("--quiet printed worker output:\n%s", out)
	}

	// A run not made by work has nothing to follow.
	runIn(dir, false, "check", "readme") // fails: the README is only on the work branch
	if _, stderr, code := runIn(dir, false, "show", "--follow"); code != ExitUsage || !strings.Contains(stderr, "nothing to follow") {
		t.Fatalf("check run: exit %d\n%s", code, stderr)
	}
}

// A project made before the rename moves over with assay init, keeping its
// runs and work-run workspaces.
func TestInitMigratesLegacyProject(t *testing.T) {
	dir := workCLIProject(t, `cat >/dev/null; echo hi > README.md`)
	mustRun(t, dir, "work", "readme", "--task", "x", "--quiet")
	// Put the project back in its pre-rename layout.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Rename(filepath.Join(dir, ".assay", "assay.db"), filepath.Join(dir, ".assay", "harness.db")))
	os.Remove(filepath.Join(dir, ".assay", "assay.db-wal"))
	os.Remove(filepath.Join(dir, ".assay", "assay.db-shm"))
	must(os.Rename(filepath.Join(dir, ".assay"), filepath.Join(dir, ".harness")))
	must(os.Rename(filepath.Join(dir, "assay"), filepath.Join(dir, "harness")))
	must(os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules\n/.harness/\n"), 0644))
	legacy, err := storage.Open(filepath.Join(dir, ".harness", "harness.db"))
	must(err)
	_, err = legacy.MoveWorkspaces(filepath.Join(dir, ".assay"), filepath.Join(dir, ".harness"))
	must(err)
	legacy.Close()

	if _, stderr, code := runIn(dir, false, "runs"); code == ExitOK || !strings.Contains(stderr, `run "assay init" here to move it over`) {
		t.Fatalf("legacy project: exit %d\n%s", code, stderr)
	}
	out := mustRun(t, dir, "init")
	for _, want := range []string{"moved    .harness/ → .assay/", "moved    harness/ → assay/", "moved    .gitignore: /.harness/ → /.assay/", "moved    1 work runs' workspace paths updated"} {
		if !strings.Contains(out, want) {
			t.Errorf("init lacks %q:\n%s", want, out)
		}
	}
	if gitignore, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); string(gitignore) != "node_modules\n/.assay/\n" {
		t.Fatalf(".gitignore: %q", gitignore)
	}
	var shown struct {
		Run runJSON `json:"run"`
	}
	json.Unmarshal([]byte(mustRun(t, dir, "show", "--json")), &shown)
	workspace := shown.Run.Work.Workspace
	if !strings.Contains(workspace, filepath.Join(".assay", "workspaces")) {
		t.Fatalf("workspace path: %s", workspace)
	}
	if out, err := exec.Command("git", "-C", workspace, "status", "--short").CombinedOutput(); err != nil {
		t.Fatalf("the moved workspace isn't a working git worktree: %v\n%s", err, out)
	}
	if out := mustRun(t, dir, "workspace", "clean"); !strings.Contains(out, "removed  .assay/workspaces/") {
		t.Fatalf("clean after migration:\n%s", out)
	}
}
