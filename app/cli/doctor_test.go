package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctor(t *testing.T) {
	// Outside a project: machine checks only, and a hint to run init.
	stdout, _, code := runIn(t.TempDir(), false, "doctor")
	if code != ExitOK || !strings.Contains(stdout, "✓ git: ") || !strings.Contains(stdout, `run "assay init"`) || !strings.Contains(stdout, "Ready, with ") {
		t.Fatalf("outside a project: exit %d\n%s", code, stdout)
	}

	dir := workCLIProject(t, `cat >/dev/null`)
	stdout = mustRun(t, dir, "doctor")
	for _, want := range []string{"✓ git repository", "✓ .assay/config.yaml: 0 commands, 0 reviewers, 1 worker", "✓ worker writer: sh -c cat >/dev/null", "✓ contracts: 2 contract files, all valid", "! sandbox: off"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor lacks %q:\n%s", want, stdout)
		}
	}

	// Things to fix: a missing program, a missing key, a not-logged-in
	// Codex, and a broken contract.
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"), 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TYPESAFE_API_KEY", "")
	original := codexLoginStatus
	codexLoginStatus = func(string) (string, error) { return "Not logged in", errors.New("exit status 1") }
	t.Cleanup(func() { codexLoginStatus = original })
	os.WriteFile(filepath.Join(dir, ".assay", "config.yaml"), []byte(`version: 1
sandbox: off
commands:
  lint: {run: [no-such-linter]}
reviewers:
  jev: {protocol: typesafe, endpoint: "https://doctor.invalid/v1"}
workers:
  codex: {run: [codex, exec, "-"], sandbox: off}
`), 0600)
	os.WriteFile(filepath.Join(dir, "assay", "broken.yaml"), []byte("id: broken\n"), 0644)
	stdout, _, code = runIn(dir, false, "doctor")
	if code != ExitNegative {
		t.Fatalf("exit %d\n%s", code, stdout)
	}
	for _, want := range []string{
		"✗ command lint: no-such-linter isn't installed or isn't on PATH",
		"✗ reviewer jev: no API key for doctor.invalid",
		"fix: assay reviewer login jev (or set TYPESAFE_API_KEY)",
		"✗ worker codex: Codex isn't logged in: Not logged in",
		"fix: run: codex login",
		"✗ contract assay/broken.yaml: ",
		"fix: assay contract validate assay/broken.yaml",
		"4 problems to fix",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor lacks %q:\n%s", want, stdout)
		}
	}

	stdout, _, code = runIn(dir, false, "doctor", "--json")
	var out struct {
		Schema int           `json:"schema"`
		OK     bool          `json:"ok"`
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || code != ExitNegative || out.Schema != JSONSchema || out.OK || len(out.Checks) == 0 {
		t.Fatalf("--json: exit %d %v\n%s", code, err, stdout)
	}

	// A config that doesn't parse is reported, not a crash.
	os.WriteFile(filepath.Join(dir, ".assay", "config.yaml"), []byte("version: 1\nnope: true\n"), 0600)
	if stdout, _, code = runIn(dir, false, "doctor"); code != ExitNegative || !strings.Contains(stdout, "✗ .assay/config.yaml: ") {
		t.Fatalf("bad config: exit %d\n%s", code, stdout)
	}
	if _, _, code = runIn(dir, false, "doctor", "extra"); code != ExitUsage {
		t.Fatalf("extra argument: exit %d", code)
	}
}
