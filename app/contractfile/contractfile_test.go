package contractfile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"assay/backend"
)

const valid = `version: 1
id: docs
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

func TestParseValid(t *testing.T) {
	result := Parse([]byte(valid))
	if !result.Valid() {
		t.Fatalf("problems: %v", result.Problems)
	}
	want := backend.Contract{ID: "docs", Name: "Documentation", Rules: []backend.Rule{
		{ID: "readme", Criterion: "Project has a README", Kind: backend.FileExists, Path: "README.md"},
		{ID: "guide", Criterion: "Project has a guide", Kind: backend.FileExists, Path: "docs/guide.md", DependsOn: []string{"readme"}},
	}}
	if !reflect.DeepEqual(result.Contract, want) {
		t.Fatalf("contract: %+v", result.Contract)
	}
}

func TestParseProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []string // Problem.String() values, in order
	}{
		{
			name: "typo and cycle are both reported",
			src:  "id: docs\nname: Docs\nrules:\n  - id: a\n    criterion: A\n    check: file_exists\n    path: A.md\n    depends_on: [b]\n  - id: b\n    criterion: B\n    check: file_exists\n    path: B.md\n    depend_on: [a]\n    depends_on: [a]\n",
			want: []string{`13: unknown key "depend_on"`, `14:5: rule "b": dependency cycle a → b → a`},
		},
		{
			name: "duplicate key",
			src:  "id: docs\nid: other\nname: x\nrules: []\n",
			want: []string{`2: mapping key "id" already defined at line 1`},
		},
		{
			name: "wrong types are not repeated as missing fields",
			src:  "id: docs\nname: [a]\nversion: abc\nrules: {a: 1}\n",
			want: []string{"2: expected text, found a list", "3: expected a whole number, found text", "4: expected a list, found a mapping"},
		},
		{
			name: "syntax error",
			src:  "id: docs\nname: : :\n",
			want: []string{"2: mapping values are not allowed in this context"},
		},
		{
			name: "second document",
			src:  valid + "---\nid: other\n",
			want: []string{"15:1: a file holds one contract; found a second YAML document"},
		},
		{
			name: "empty file",
			src:  "",
			want: []string{"file is empty; expected a contract"},
		},
		{
			name: "not a mapping",
			src:  "- id: docs\n",
			want: []string{"1:1: expected a contract mapping with id, name, and rules"},
		},
		{
			name: "unsupported version",
			src:  strings.Replace(valid, "version: 1", "version: 2", 1),
			want: []string{"1:1: unsupported file version 2; this version of assay reads version 1"},
		},
		{
			name: "rule problems point at the field or the rule",
			src:  "id: a\nname: b\nrules:\n  - id: r\n    check: file_exists\n    path: ../x\n",
			want: []string{`4:5: rule "r": acceptance criterion is required`, `6:5: rule "r": path must be a relative path inside the project`},
		},
		{
			name: "unsupported check",
			src:  strings.Replace(valid, "check: file_exists\n    path: README.md", "check: command\n    path: README.md", 1),
			want: []string{`7:5: rule "readme": unsupported check "command"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := Parse([]byte(tc.src))
			got := make([]string, len(result.Problems))
			for i, p := range result.Problems {
				got[i] = p.String()
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("problems:\n got  %q\n want %q", got, tc.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(good, []byte(valid), 0600)
	os.WriteFile(bad, []byte("id: x\n"), 0600)

	if c, err := Load(good); err != nil || c.ID != "docs" {
		t.Fatalf("load good: %+v %v", c, err)
	}
	_, err := Load(bad)
	var invalid *InvalidError
	if !errors.As(err, &invalid) || len(invalid.Problems) != 2 || !strings.HasPrefix(err.Error(), bad+":") {
		t.Fatalf("load bad: %v", err)
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load missing: %v", err)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	original := Parse([]byte(valid)).Contract
	data, err := Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != valid {
		t.Errorf("marshalled form differs from the documented format:\n%s", data)
	}
	again := Parse(data)
	if !again.Valid() || !backend.SameContent(original, again.Contract) {
		t.Fatalf("round trip: %+v %v", again.Contract, again.Problems)
	}
}

func TestEvidence(t *testing.T) {
	src := valid + `    evidence:
      - file: docs/guide.md
      - diff: {}
      - diff: {paths: [docs]}
`
	result := Parse([]byte(src))
	if !result.Valid() {
		t.Fatalf("problems: %v", result.Problems)
	}
	want := []backend.EvidenceSpec{
		{Kind: backend.FileEvidence, Path: "docs/guide.md"},
		{Kind: backend.DiffEvidence},
		{Kind: backend.DiffEvidence, Paths: []string{"docs"}},
	}
	if got := result.Contract.Rules[1].Evidence; !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence: %+v", got)
	}
	data, err := Marshal(result.Contract)
	if err != nil || string(data) != src {
		t.Fatalf("marshal round trip:\n%s", data)
	}

	bad := Parse([]byte(valid + `    evidence:
      - diff:
      - {file: a, diff: {}}
      - file: ../outside
      - command: "go test"
      - shell: ls
`))
	got := make([]string, len(bad.Problems))
	for i, p := range bad.Problems {
		got[i] = p.String()
	}
	both := "each evidence item needs exactly one of file: <path>, diff: {} (diff: {paths: [...]} limits it), or command: <name>"
	wantProblems := []string{
		`14:5: rule "guide": file evidence path "../outside" must be a relative path inside the project`,
		`14:5: rule "guide": command evidence "go test" isn't a valid command name`,
		`15:9: rule "guide": ` + both,
		`16:9: rule "guide": ` + both,
		`19: unknown key "shell"`,
	}
	if !reflect.DeepEqual(got, wantProblems) {
		t.Fatalf("problems:\n got  %q\n want %q", got, wantProblems)
	}
}

func TestCommandRules(t *testing.T) {
	src := `version: 1
id: ci
name: CI
rules:
  - id: tests
    criterion: Tests pass
    check: command_succeeds
    command: go-test
    evidence:
      - command: go-vet
`
	result := Parse([]byte(src))
	if !result.Valid() {
		t.Fatalf("problems: %v", result.Problems)
	}
	rule := result.Contract.Rules[0]
	if rule.Kind != backend.CommandSucceeds || rule.Command != "go-test" || !reflect.DeepEqual(rule.Evidence[0], backend.EvidenceSpec{Kind: backend.CommandEvidence, Command: "go-vet"}) {
		t.Fatalf("rule: %+v", rule)
	}
	if data, _ := Marshal(result.Contract); string(data) != src {
		t.Fatalf("round trip:\n%s", data)
	}

	// Commands are checked against a project's config separately.
	result.AddValidation(backend.ValidateCommands(result.Contract, backend.Config{Commands: map[string]backend.CommandSpec{"go-vet": {}}}))
	if len(result.Problems) != 1 || result.Problems[0].String() != `8:5: rule "tests": command "go-test" isn't approved in .assay/config.yaml` {
		t.Fatalf("unapproved command: %v", result.Problems)
	}

	bad := Parse([]byte(strings.Replace(src, "command: go-test", "command: go-test\n    path: x.txt", 1)))
	if len(bad.Problems) != 1 || !strings.Contains(bad.Problems[0].Message, "path isn't used by command_succeeds") {
		t.Fatalf("path on a command rule: %v", bad.Problems)
	}
	bad = Parse([]byte(strings.Replace(src, "command: go-test", "command: \"\"", 1)))
	if len(bad.Problems) != 1 || !strings.Contains(bad.Problems[0].Message, "needs the name of an approved command") {
		t.Fatalf("missing command: %v", bad.Problems)
	}
}

func TestReviewRules(t *testing.T) {
	src := `version: 1
id: docs
name: Docs
rules:
  - id: install
    criterion: README explains how to install
    check: review
    reviewer: careful
    evidence:
      - file: README.md
  - id: tone
    criterion: README is friendly
    check: review
    evidence:
      - file: README.md
`
	result := Parse([]byte(src))
	if !result.Valid() {
		t.Fatalf("problems: %v", result.Problems)
	}
	rule := result.Contract.Rules[0]
	if rule.Kind != backend.ReviewCheck || rule.Reviewer != "careful" || result.Contract.Rules[1].Reviewer != "" {
		t.Fatalf("rules: %+v", result.Contract.Rules)
	}
	if data, _ := Marshal(result.Contract); string(data) != src {
		t.Fatalf("round trip:\n%s", data)
	}
	changed := Parse([]byte(strings.Replace(src, "reviewer: careful", "reviewer: other", 1))).Contract
	if backend.SameContent(result.Contract, changed) {
		t.Fatal("a different reviewer is different content")
	}

	// Reviewers are checked against a project's config separately.
	config := backend.DefaultConfig()
	config.Reviewers["careful"] = backend.ReviewerSpec{Name: "careful", Protocol: backend.CommandProtocol}
	config.Reviewers["quick"] = backend.ReviewerSpec{Name: "quick", Protocol: backend.CommandProtocol}
	result.AddValidation(backend.ValidateReviewers(result.Contract, config))
	if len(result.Problems) != 1 || result.Problems[0].String() != `13:5: rule "tone": the rule doesn't name a reviewer and .assay/config.yaml sets no default_reviewer` {
		t.Fatalf("no default: %v", result.Problems)
	}
	config.DefaultReviewer = "quick"
	if err := backend.ValidateReviewers(Parse([]byte(src)).Contract, config); err != nil {
		t.Fatalf("with a default: %v", err)
	}

	bad := Parse([]byte(`id: docs
name: Docs
rules:
  - id: a
    criterion: A
    check: review
    path: README.md
    reviewer: "not ok"
  - id: b
    criterion: B
    check: file_exists
    path: B.md
    reviewer: careful
`))
	got := make([]string, len(bad.Problems))
	for i, p := range bad.Problems {
		got[i] = p.String()
	}
	want := []string{
		`6:5: rule "a": a review needs evidence to judge; list what the reviewer should see under evidence`,
		`7:5: rule "a": path isn't used by review; list files under evidence`,
		`8:5: rule "a": reviewer "not ok" isn't a valid reviewer name (letters, digits, '.', '_', '-')`,
		`13:5: rule "b": reviewer is only used by review`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("problems:\n got  %q\n want %q", got, want)
	}
}
