package cli

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/golden")

const docsContract = `id: docs
name: Documentation
rules:
  - id: readme
    criterion: Project has a README
    check: file_exists
    path: README.md
  - id: guide
    criterion: Project has a guide
    check: file_exists
    path: docs/guide.md
    depends_on: [readme]
`

// newProject creates a project with docs@1 published.
func newProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "init")
	os.WriteFile(filepath.Join(dir, "harness", "docs.yaml"), []byte(docsContract), 0644)
	mustRun(t, dir, "contract", "publish", "harness/docs.yaml", "--yes")
	return dir
}

var (
	runIDPattern = regexp.MustCompile(`\b[a-z2-7]{26}\b`)
	shortPattern = regexp.MustCompile(`"shortId": "[a-z2-7]{6}"`)
	timePattern  = regexp.MustCompile(`"\d{4}-\d\d-\d\dT[0-9:.]+Z"`)
)

// golden compares JSON output with testdata/golden/<name>.json after
// replacing values that differ on every run.
func golden(t *testing.T, name, dir, got string) {
	t.Helper()
	got = strings.ReplaceAll(got, dir, "PROJECT")
	got = runIDPattern.ReplaceAllString(got, "RUN_ID")
	got = shortPattern.ReplaceAllString(got, `"shortId": "SHORT"`)
	got = timePattern.ReplaceAllString(got, `"TIME"`)
	path := filepath.Join("testdata", "golden", name+".json")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./cli -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s (run go test ./cli -update if the change is intended):\n%s", name, path, got)
	}
}

func TestFailCorrectPassKeepsHistory(t *testing.T) {
	dir := newProject(t)

	stdout, _, code := runIn(dir, false, "check", "docs")
	if code != ExitNegative {
		t.Fatalf("check: exit %d\n%s", code, stdout)
	}
	short := regexp.MustCompile(`run ([a-z2-7]{6})`).FindStringSubmatch(stdout)[1]
	want := "docs@1 · run " + short + ` · attempt 1 · FAILED

  ✗ readme   README.md does not exist
  ⊘ guide    blocked: dependency readme did not pass

1 failed, 1 blocked · fix, then: harness recheck ` + short + "\n"
	if stdout != want {
		t.Fatalf("check output:\n%s\nwant:\n%s", stdout, want)
	}

	// A newer version doesn't change what the run checks.
	os.WriteFile(filepath.Join(dir, "harness", "docs.yaml"), []byte(strings.Replace(docsContract, "README.md", "README.txt", 1)), 0644)
	mustRun(t, dir, "contract", "publish", "harness/docs.yaml", "--yes")

	os.WriteFile(filepath.Join(dir, "README.md"), nil, 0644)
	os.MkdirAll(filepath.Join(dir, "docs"), 0755)
	os.WriteFile(filepath.Join(dir, "docs", "guide.md"), nil, 0644)
	sub := filepath.Join(dir, "docs")
	stdout, _, code = runIn(sub, false, "recheck", strings.ToUpper(short[:3]))
	if code != ExitOK || !strings.Contains(stdout, "docs@1 · run "+short+" · attempt 2 · PASSED") || !strings.Contains(stdout, "all 2 rules passed") {
		t.Fatalf("recheck: exit %d\n%s", code, stdout)
	}

	show := mustRun(t, dir, "show")
	for _, want := range []string{"run " + short + " · docs@1 · ", "passed on attempt 2", "attempt 1 · failed", "✗ readme   README.md does not exist", "attempt 2 · passed"} {
		if !strings.Contains(show, want) {
			t.Errorf("show lacks %q:\n%s", want, show)
		}
	}
	only := mustRun(t, dir, "show", short, "--attempt", "1")
	if strings.Contains(only, "attempt 2 ·") || !strings.Contains(only, "attempt 1 · failed") {
		t.Errorf("show --attempt 1:\n%s", only)
	}

	stdout, _, _ = runIn(dir, false, "show", "--json")
	golden(t, "show", dir, stdout)
	runs := mustRun(t, dir, "runs")
	if !regexp.MustCompile(`(?m)^RUN\s+CONTRACT\s+STATUS\s+ATTEMPTS\s+STARTED\n` + short + `\s+docs@1\s+passed\s+2\s+\d{4}-`).MatchString(runs) {
		t.Errorf("runs:\n%s", runs)
	}
}

func TestCheckJSONAndExitCodes(t *testing.T) {
	dir := newProject(t)

	stdout, _, code := runIn(dir, false, "check", "docs@1", "--json")
	if code != ExitNegative {
		t.Fatalf("exit %d", code)
	}
	golden(t, "check-failed", dir, stdout)

	os.WriteFile(filepath.Join(dir, "README.md"), nil, 0644)
	os.MkdirAll(filepath.Join(dir, "docs", "guide.md"), 0755) // a directory, not a file
	stdout, _, code = runIn(dir, false, "recheck")
	if code != ExitNegative || !strings.Contains(stdout, "docs/guide.md is not a regular file") {
		t.Fatalf("directory instead of file: exit %d\n%s", code, stdout)
	}

	// A check that can't run is an operational error, not a failure.
	os.Remove(filepath.Join(dir, "README.md"))
	os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(dir, "README.md"))
	stdout, _, code = runIn(dir, false, "recheck")
	if code != ExitOperational || !strings.Contains(stdout, "! readme") || !strings.Contains(stdout, "1 couldn't be checked") {
		t.Fatalf("operational error: exit %d\n%s", code, stdout)
	}
}

func TestRunsFiltersAndLookupErrors(t *testing.T) {
	dir := newProject(t)
	mustRun(t, dir, "init") // re-running init doesn't disturb anything
	for range 3 {
		runIn(dir, false, "check", "docs")
	}
	stdout := mustRun(t, dir, "runs", "--limit", "2")
	if lines := strings.Count(stdout, "\n"); lines != 3 {
		t.Errorf("runs --limit 2 printed %d lines:\n%s", lines, stdout)
	}
	if out := mustRun(t, dir, "runs", "--contract", "other"); !strings.Contains(out, "no runs yet") {
		t.Errorf("runs --contract other:\n%s", out)
	}
	if out := mustRun(t, dir, "runs", "--json"); strings.Count(out, `"shortId"`) != 3 {
		t.Errorf("runs --json:\n%s", out)
	}

	for _, tc := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"check", "nope"}, ExitUsage, `no published contract "nope"`},
		{[]string{"check", "docs@5"}, ExitUsage, "docs has no version 5"},
		{[]string{"check"}, ExitUsage, "exactly one contract"},
		{[]string{"recheck", "zzzz"}, ExitUsage, `no run matches "zzzz"`},
		{[]string{"recheck", ""}, ExitOK, ""}, // empty prefix: most recent run (fails, but checks)
		{[]string{"show", "a", "b"}, ExitUsage, "at most one run"},
		{[]string{"show", "--attempt", "40"}, ExitUsage, "attempts"},
		{[]string{"runs", "--limit", "-1"}, ExitUsage, "--limit must be 0 or more"},
		{[]string{"runs", "extra"}, ExitUsage, `unexpected argument "extra"`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, stderr, code := runIn(dir, false, tc.args...)
			if tc.code == ExitOK {
				if code != ExitNegative { // the project has no README, so checks fail
					t.Fatalf("exit %d: %s", code, stderr)
				}
				return
			}
			if code != tc.code || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("exit %d (want %d): %s", code, tc.code, stderr)
			}
		})
	}
}

func TestAmbiguousRunPrefix(t *testing.T) {
	dir := newProject(t)
	// With enough runs, some share a first character; an empty project is
	// cheap, so make runs until two collide.
	seen := map[byte]bool{}
	var collision byte
	for collision == 0 {
		stdout, _, _ := runIn(dir, false, "check", "docs")
		first := regexp.MustCompile(`run ([a-z2-7])`).FindStringSubmatch(stdout)[1][0]
		if seen[first] {
			collision = first
		}
		seen[first] = true
	}
	_, stderr, code := runIn(dir, false, "show", string(collision))
	if code != ExitUsage || !strings.Contains(stderr, "matches") || !strings.Contains(stderr, "use more characters") {
		t.Fatalf("ambiguous prefix: exit %d %s", code, stderr)
	}
}
