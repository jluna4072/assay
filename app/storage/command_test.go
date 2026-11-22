package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"assay/backend"
)

func requireSandbox(t *testing.T) {
	t.Helper()
	if err := backend.SandboxAvailable(); err != nil {
		t.Skipf("sandbox unavailable: %v", err)
	}
}

// commandRun publishes a contract whose rules run the given approved
// commands, then checks it once.
func commandRun(t *testing.T, config backend.Config, project string, rules ...backend.Rule) backend.Run {
	t.Helper()
	store, s := openTest(t)
	s.LoadConfig = func(string) (backend.Config, error) { return config, nil }
	publish(t, s, backend.Contract{ID: "ci", Name: "CI", Rules: rules})
	run, err := s.StartRun("ci", 1, project)
	if err != nil {
		t.Fatal(err)
	}
	runStores[run.ID] = store
	run, err = s.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func commandRule(id, command string) backend.Rule {
	return backend.Rule{ID: id, Criterion: id, Kind: backend.CommandSucceeds, Command: command}
}

func sh(script string) []string { return []string{"/bin/sh", "-c", script} }

func configWith(sandbox bool, specs ...backend.CommandSpec) backend.Config {
	config := backend.Config{Sandbox: sandbox, Commands: map[string]backend.CommandSpec{}}
	for _, spec := range specs {
		config.Commands[spec.Name] = spec
	}
	return config
}

func TestCommandSucceedsRecordsOutputAndExitStatus(t *testing.T) {
	requireSandbox(t)
	project := t.TempDir()
	run := commandRun(t, configWith(true,
		backend.CommandSpec{Name: "ok", Run: sh("echo out; echo err >&2")},
		backend.CommandSpec{Name: "fails", Run: sh("echo nope; exit 3")},
		backend.CommandSpec{Name: "missing", Run: []string{"no-such-program-xyz"}},
	), project, commandRule("ok", "ok"), commandRule("fails", "fails"), commandRule("missing", "missing"), commandRule("unknown", "not-approved"))

	results := run.Attempts[0].Results
	ok, fails, missing, unknown := results[0], results[1], results[2], results[3]
	if ok.Status != backend.Passed || len(ok.Evidence) != 1 || !ok.Evidence[0].Sandboxed || ok.Evidence[0].ExitCode != 0 {
		t.Fatalf("ok: %+v", ok)
	}
	if fails.Status != backend.Failed || fails.Reason != "fails exited with status 3" || fails.Evidence[0].ExitCode != 3 {
		t.Fatalf("fails: %+v", fails)
	}
	if missing.Status != backend.OperationalError || !strings.Contains(missing.Reason, `can't find "no-such-program-xyz"`) {
		t.Fatalf("missing program: %+v", missing)
	}
	if unknown.Status != backend.OperationalError || !strings.Contains(unknown.Reason, `command "not-approved" isn't approved`) {
		t.Fatalf("unapproved: %+v", unknown)
	}
	if run.Attempts[0].Status != backend.OperationalError {
		t.Fatalf("attempt status %s", run.Attempts[0].Status)
	}
}

func TestSandboxLimitsCommands(t *testing.T) {
	requireSandbox(t)
	home, _ := os.UserHomeDir()
	project := t.TempDir()
	os.MkdirAll(filepath.Join(project, ".assay"), 0700)
	os.MkdirAll(filepath.Join(project, ".git", "hooks"), 0700)
	os.WriteFile(filepath.Join(project, ".assay", "assay.db"), []byte("state"), 0600)
	t.Setenv("ASSAY_TEST_SECRET", "s3cret")
	t.Setenv("ASSAY_TEST_ALLOWED", "visible")
	probe := filepath.Join(home, ".assay-sandbox-probe")
	t.Cleanup(func() { os.Remove(probe) })

	script := strings.Join([]string{
		`echo x > "$TMPDIR/scratch" && echo scratch-write-ok`,
		`echo x > "` + probe + `" 2>/dev/null || echo home-write-denied`,
		`echo x > project.txt 2>/dev/null || echo project-write-denied`,
		`cat .assay/assay.db >/dev/null 2>&1 || echo state-read-denied`,
		`/usr/bin/curl -sS -m 3 https://example.com >/dev/null 2>&1 || echo network-denied`,
		`echo "secret=[$ASSAY_TEST_SECRET] allowed=[$ASSAY_TEST_ALLOWED]"`,
		`echo x > "$GOCACHE/entry" && echo cache-write-ok`,
	}, "; ")
	run := commandRun(t, configWith(true,
		backend.CommandSpec{Name: "probe", Run: sh(script), Env: []string{"ASSAY_TEST_ALLOWED"}, Caches: []string{"GOCACHE"}},
		backend.CommandSpec{Name: "writer", Run: sh(`echo x > project.txt && echo project-write-ok; echo x > .assay/evil 2>/dev/null || echo state-write-denied; echo x > .git/hooks/pre-commit 2>/dev/null || echo git-write-denied`), WritesProject: true},
	), project, commandRule("probe", "probe"), commandRule("writer", "writer"))

	output := func(i int) string {
		t.Helper()
		item := run.Attempts[0].Results[i].Evidence[0]
		return item.Command + ": " + string(mustBlob(t, run, item))
	}
	for _, want := range []string{"scratch-write-ok", "home-write-denied", "project-write-denied", "state-read-denied", "network-denied", "secret=[] allowed=[visible]", "cache-write-ok"} {
		if !strings.Contains(output(0), want) {
			t.Errorf("probe output lacks %q:\n%s", want, output(0))
		}
	}
	for _, want := range []string{"project-write-ok", "state-write-denied", "git-write-denied"} {
		if !strings.Contains(output(1), want) {
			t.Errorf("writer output lacks %q:\n%s", want, output(1))
		}
	}
	if _, err := os.Stat(probe); err == nil {
		t.Error("sandboxed command wrote to the home directory")
	}
	if entries, _ := os.ReadDir(filepath.Join(project, ".assay", "tmp")); len(entries) != 0 {
		t.Errorf("scratch left behind: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(project, ".assay", "cache", "probe", "GOCACHE", "entry")); err != nil {
		t.Error("cache directory didn't persist")
	}
}

// blobs are stored per test database; keep the run's store reachable.
var runStores = map[string]*SQLite{}

func mustBlob(t *testing.T, run backend.Run, item backend.Evidence) []byte {
	t.Helper()
	store := runStores[run.ID]
	if store == nil {
		t.Fatalf("no store recorded for run %s", run.ID)
	}
	data, err := store.GetBlob(item.Blob)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCommandTimeoutStopsChildren(t *testing.T) {
	requireSandbox(t)
	project := t.TempDir()
	marker := filepath.Join(project, ".assay", "cache", "slow", "MARK", "survived")
	started := time.Now()
	run := commandRun(t, configWith(true, backend.CommandSpec{
		Name: "slow", Timeout: 1 * time.Second, Caches: []string{"MARK"},
		Run: sh(`(sleep 3; touch "$MARK/survived") & sleep 30`),
	}), project, commandRule("slow", "slow"))
	result := run.Attempts[0].Results[0]
	if result.Status != backend.Failed || !result.Evidence[0].TimedOut || !strings.Contains(result.Reason, "slow timed out after 1s") {
		t.Fatalf("timeout: %+v", result)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a child process outlived the timeout")
	}
}

func TestCommandRunsOncePerAttempt(t *testing.T) {
	requireSandbox(t)
	project := t.TempDir()
	counter := backend.CommandSpec{Name: "count", Run: sh(`echo run >> "$COUNT/runs"`), Caches: []string{"COUNT"}}
	rule := commandRule("a", "count")
	rule.Evidence = []backend.EvidenceSpec{{Kind: backend.CommandEvidence, Command: "count"}}
	other := backend.Rule{ID: "b", Criterion: "b", Kind: backend.FileExists, Path: "x", Evidence: []backend.EvidenceSpec{{Kind: backend.CommandEvidence, Command: "count"}}}
	run := commandRun(t, configWith(true, counter), project, rule, other)
	data, _ := os.ReadFile(filepath.Join(project, ".assay", "cache", "count", "COUNT", "runs"))
	if strings.Count(string(data), "run") != 1 {
		t.Fatalf("command ran %d times", strings.Count(string(data), "run"))
	}
	if len(run.Attempts[0].Results[0].Evidence) != 1 || run.Attempts[0].Results[1].Evidence[0].Command != "count" {
		t.Fatalf("evidence: %+v", run.Attempts[0].Results)
	}
}

func TestUnsandboxedAndUnavailableSandbox(t *testing.T) {
	project := t.TempDir()
	run := commandRun(t, configWith(false, backend.CommandSpec{Name: "ok", Run: sh("exit 0")}), project, commandRule("ok", "ok"))
	if item := run.Attempts[0].Results[0].Evidence[0]; run.Attempts[0].Results[0].Status != backend.Passed || item.Sandboxed {
		t.Fatalf("sandbox off: %+v", run.Attempts[0].Results[0])
	}

	original := backend.SandboxAvailable
	backend.SandboxAvailable = func() error { return errors.New("no sandbox here") }
	t.Cleanup(func() { backend.SandboxAvailable = original })
	run = commandRun(t, configWith(true, backend.CommandSpec{Name: "ok", Run: sh("exit 0")}), project, commandRule("ok", "ok"))
	result := run.Attempts[0].Results[0]
	if result.Status != backend.OperationalError || !strings.Contains(result.Reason, "no sandbox here") || !strings.Contains(result.Reason, `"sandbox: off"`) {
		t.Fatalf("unavailable sandbox: %+v", result)
	}
}

func TestBrokenConfigOnlyAffectsCommandRules(t *testing.T) {
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "README.md"), nil, 0644)
	_, s := openTest(t)
	s.LoadConfig = func(string) (backend.Config, error) { return backend.Config{}, errors.New("invalid config: line 3") }
	publish(t, s, backend.Contract{ID: "ci", Name: "CI", Rules: []backend.Rule{
		{ID: "readme", Criterion: "c", Kind: backend.FileExists, Path: "README.md"},
		commandRule("tests", "go-test"),
	}})
	run, _ := s.StartRun("ci", 1, project)
	run, err := s.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r := run.Attempts[0].Results; r[0].Status != backend.Passed || r[1].Status != backend.OperationalError || !strings.Contains(r[1].Reason, "invalid config") {
		t.Fatalf("results: %+v", r)
	}
}
