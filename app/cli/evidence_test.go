package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"harness/backend"
)

const evidenceContract = `id: docs
name: Documentation
rules:
  - id: readme
    criterion: Project has a README
    check: file_exists
    path: README.md
    evidence:
      - file: README.md
  - id: guide
    criterion: Project has a guide
    check: file_exists
    path: docs/guide.md
    depends_on: [readme]
    evidence:
      - file: docs/guide.md
      - diff: {paths: [docs]}
`

// evidenceProject is a git repository with a committed README and docs@1
// (with evidence) published.
func evidenceProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Demo\n"), 0644)
	mustRun(t, dir, "init")
	os.WriteFile(filepath.Join(dir, "harness", "docs.yaml"), []byte(evidenceContract), 0644)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "start"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustRun(t, dir, "contract", "publish", "harness/docs.yaml", "--yes")
	return dir
}

var commitPattern = regexp.MustCompile(`\b[0-9a-f]{40}\b|\b[0-9a-f]{7}\b|sha256 [0-9a-f]{12}`)

func TestEvidenceIsKeptPerAttempt(t *testing.T) {
	dir := evidenceProject(t)
	if _, _, code := runIn(dir, false, "check", "docs"); code != ExitNegative {
		t.Fatalf("check: exit %d", code)
	}
	os.MkdirAll(filepath.Join(dir, "docs"), 0755)
	os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte("How to use it.\n"), 0644)
	mustRun(t, dir, "recheck")
	// Editing the file afterwards doesn't change what attempt 2 recorded.
	os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte("Changed later.\n"), 0644)

	if out := mustRun(t, dir, "evidence", "guide", "--item", "1"); out != "How to use it.\n" {
		t.Fatalf("evidence --item 1: %q", out)
	}
	all := mustRun(t, dir, "evidence", "guide")
	for _, want := range []string{"==> [1] file docs/guide.md · 15 B · sha256 ", "How to use it.\n\n==> [2] diff since ", " of docs · 1 file changed", "+How to use it."} {
		if !strings.Contains(all, want) {
			t.Errorf("evidence guide lacks %q:\n%s", want, all)
		}
	}

	stdout, _, code := runIn(dir, false, "evidence", "guide", "--attempt", "1")
	if code != ExitNegative || !strings.Contains(stdout, "[1] file docs/guide.md · absent") || !strings.Contains(stdout, "0 files changed") {
		t.Fatalf("attempt 1 evidence: exit %d\n%s", code, stdout)
	}

	show := mustRun(t, dir, "show", "--evidence")
	show = commitPattern.ReplaceAllString(show, "HASH")
	for _, want := range []string{
		"started ", " at HASH · passed on attempt 2",
		"  ✗ guide    docs/guide.md does not exist\n      [1] file docs/guide.md · absent\n      [2] diff since HASH of docs · 0 files changed, 0 B · HASH\n",
		"  ✓ readme\n      [1] file README.md · 7 B · HASH\n",
		"Print an item with: harness evidence ",
	} {
		if !strings.Contains(show, want) {
			t.Errorf("show --evidence lacks %q:\n%s", want, show)
		}
	}
	if plain := mustRun(t, dir, "show"); strings.Contains(plain, "[1]") {
		t.Errorf("show without --evidence lists evidence:\n%s", plain)
	}

	stdout, _, _ = runIn(dir, false, "show", "--json", "--attempt", "2")
	golden(t, "show-evidence", dir, commitPattern.ReplaceAllString(stdout, "HASH"))
	stdout, _, _ = runIn(dir, false, "evidence", "guide", "--json")
	golden(t, "evidence", dir, commitPattern.ReplaceAllString(stdout, "HASH"))
}

func TestEvidenceErrors(t *testing.T) {
	dir := evidenceProject(t)
	runIn(dir, false, "check", "docs") // guide fails: no docs/guide.md
	os.Remove(filepath.Join(dir, "README.md"))
	runIn(dir, false, "recheck") // readme fails, so guide is blocked

	for _, tc := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"evidence"}, ExitUsage, "name a rule"},
		{[]string{"evidence", "a", "b", "c"}, ExitUsage, "name a rule"},
		{[]string{"evidence", "nope"}, ExitUsage, `docs@1 has no rule "nope"`},
		{[]string{"evidence", "guide"}, ExitNegative, "it was blocked, so it wasn't checked"},
		{[]string{"evidence", "guide", "--attempt", "3"}, ExitUsage, "has 2 attempts"},
		{[]string{"evidence", "guide", "--attempt", "1", "--item", "3"}, ExitUsage, "has 2 evidence items"},
		{[]string{"evidence", "guide", "--attempt", "1", "--item", "1"}, ExitNegative, "file docs/guide.md · absent"},
		{[]string{"evidence", "zzzz", "guide"}, ExitUsage, `no run matches "zzzz"`},
		{[]string{"evidence", "readme", "--attempt", "-1"}, ExitUsage, "positive numbers"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, stderr, code := runIn(dir, false, tc.args...)
			if code != tc.code || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("exit %d (want %d): %s", code, tc.code, stderr)
			}
		})
	}

	// A rule with no evidence configured.
	plain := newProject(t)
	runIn(plain, false, "check", "docs")
	if _, stderr, code := runIn(plain, false, "evidence", "readme"); code != ExitNegative || !strings.Contains(stderr, "its contract asks for none") {
		t.Fatalf("no evidence configured: exit %d %s", code, stderr)
	}
}

func TestCommandRulesThroughTheCLI(t *testing.T) {
	if err := backend.SandboxAvailable(); err != nil {
		t.Skipf("sandbox unavailable: %v", err)
	}
	dir := t.TempDir()
	mustRun(t, dir, "init")
	contract := filepath.Join(dir, "harness", "ci.yaml")
	os.WriteFile(contract, []byte(`id: ci
name: CI
rules:
  - id: flag
    criterion: The flag file says yes
    check: command_succeeds
    command: check-flag
`), 0644)

	// Not approved yet: validate and publish both refuse.
	stdout, _, code := runIn(dir, false, "contract", "validate", contract)
	if code != ExitNegative || !strings.Contains(stdout, `command "check-flag" isn't approved in .harness/config.yaml`) {
		t.Fatalf("validate unapproved: exit %d\n%s", code, stdout)
	}
	if _, _, code := runIn(dir, false, "contract", "publish", contract, "--yes"); code != ExitNegative {
		t.Fatalf("publish unapproved: exit %d", code)
	}

	config, _ := os.ReadFile(filepath.Join(dir, ".harness", "config.yaml"))
	config = []byte(strings.Replace(string(config), "commands: {}", "commands:\n  check-flag:\n    run: [/bin/sh, -c, 'cat flag.txt; grep -q yes flag.txt']", 1))
	os.WriteFile(filepath.Join(dir, ".harness", "config.yaml"), config, 0600)
	mustRun(t, dir, "contract", "validate", contract)
	mustRun(t, dir, "contract", "publish", contract, "--yes")

	os.WriteFile(filepath.Join(dir, "flag.txt"), []byte("no\n"), 0644)
	stdout, _, code = runIn(dir, false, "check", "ci")
	if code != ExitNegative || !strings.Contains(stdout, "✗ flag   check-flag exited with status 1") {
		t.Fatalf("check: exit %d\n%s", code, stdout)
	}
	os.WriteFile(filepath.Join(dir, "flag.txt"), []byte("yes\n"), 0644)
	mustRun(t, dir, "recheck")

	show := mustRun(t, dir, "show", "--evidence")
	if !regexp.MustCompile(`\[1\] command check-flag · exit 1 in \d+ms, 3 B output · sha256 [0-9a-f]{12}`).MatchString(show) ||
		!regexp.MustCompile(`\[1\] command check-flag · exit 0 in \d+ms, 4 B output`).MatchString(show) {
		t.Fatalf("show --evidence:\n%s", show)
	}
	if out := mustRun(t, dir, "evidence", "flag", "--attempt", "1", "--item", "1"); out != "no\n" {
		t.Fatalf("command output: %q", out)
	}
	json := mustRun(t, dir, "show", "--json")
	for _, want := range []string{`"kind": "command"`, `"command": "check-flag"`, `"exitCode": 1`, `"exitCode": 0`, `"sandboxed": true`, `"argv": [`} {
		if !strings.Contains(json, want) {
			t.Errorf("show --json lacks %s", want)
		}
	}
}
