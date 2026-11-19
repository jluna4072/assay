package contractfile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness/backend"
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
			want: []string{"1:1: unsupported file version 2; this harness reads version 1"},
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
