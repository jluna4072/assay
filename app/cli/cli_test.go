package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func run(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = Run(args, Env{Stdout: &out, Stderr: &errOut, Dir: "."})
	return out.String(), errOut.String(), code
}

func TestValidateText(t *testing.T) {
	stdout, stderr, code := run("contract", "validate", "testdata/typo-and-cycle.yaml", "testdata/valid.yaml")
	want := `✗ testdata/typo-and-cycle.yaml: 2 problems
  testdata/typo-and-cycle.yaml:13: unknown key "depend_on"
  testdata/typo-and-cycle.yaml:14:5: rule "guide": dependency cycle readme → guide → readme
✓ testdata/valid.yaml: docs, 2 rules
1 valid, 1 invalid
`
	if stdout != want || stderr != "" || code != ExitNegative {
		t.Fatalf("code %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

func TestValidateJSON(t *testing.T) {
	stdout, _, code := run("contract", "validate", "testdata/typo-and-cycle.yaml", "--json")
	if code != ExitNegative {
		t.Fatalf("exit %d", code)
	}
	var got struct {
		Schema int  `json:"schema"`
		Valid  bool `json:"valid"`
		Files  []struct {
			Path     string `json:"path"`
			Valid    bool   `json:"valid"`
			Problems []struct {
				Line    int    `json:"line"`
				Column  int    `json:"column"`
				Rule    string `json:"rule"`
				Message string `json:"message"`
			} `json:"problems"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	if got.Schema != JSONSchema || got.Valid || len(got.Files) != 1 || len(got.Files[0].Problems) != 2 {
		t.Fatalf("unexpected: %+v", got)
	}
	if p := got.Files[0].Problems[1]; p.Line != 14 || p.Column != 5 || p.Rule != "guide" {
		t.Fatalf("cycle problem: %+v", p)
	}

	stdout, _, code = run("contract", "validate", "--json", "testdata/valid.yaml")
	if code != ExitOK || !strings.Contains(stdout, `"problems": []`) || !strings.Contains(stdout, `"rules": 2`) {
		t.Fatalf("valid json (exit %d):\n%s", code, stdout)
	}
}

func TestExitCodesAndUsage(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"contract", "validate", "testdata/valid.yaml"}, ExitOK, "✓ testdata/valid.yaml: docs, 2 rules\n", ""},
		{[]string{"contract", "validate", "testdata/missing.yaml"}, ExitOperational, "", "no such file"},
		{[]string{"contract", "validate", "testdata/typo-and-cycle.yaml", "testdata/missing.yaml"}, ExitOperational, "0 valid, 1 invalid, 1 unreadable", ""},
		{[]string{"contract", "validate"}, ExitUsage, "", "name at least one contract file"},
		{[]string{"contract", "validate", "--bogus", "x"}, ExitUsage, "", "flag provided but not defined"},
		{[]string{"contract", "validate", "--help"}, ExitOK, "Usage:\n  assay contract validate <file>...", ""},
		{[]string{"contract", "validate", "--", "--json"}, ExitOperational, "", "open --json"},
		{[]string{}, ExitUsage, "", "Usage:"},
		{[]string{"--help"}, ExitOK, "contract validate <file>...", ""},
		{[]string{"help", "contract", "validate"}, ExitOK, "Reads each YAML contract file", ""},
		{[]string{"contract"}, ExitUsage, "", "assay contract <command>"},
		{[]string{"contract", "--help"}, ExitOK, "assay contract <command>", ""},
		{[]string{"nope"}, ExitUsage, "", `unknown command "nope"`},
		{[]string{"--version"}, ExitOK, "assay ", ""},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			stdout, stderr, code := run(tc.args...)
			if code != tc.code || !strings.Contains(stdout, tc.stdout) || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("exit %d (want %d)\nstdout:\n%s\nstderr:\n%s", code, tc.code, stdout, stderr)
			}
			if tc.stdout == "" && stdout != "" {
				t.Fatalf("unexpected stdout:\n%s", stdout)
			}
		})
	}
}
