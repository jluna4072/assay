package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// releaseProject publishes testdata/release.yaml (shared dependencies, a
// quoted criterion) and checks it once with only README.md present.
func releaseProject(t *testing.T) (dir, run string) {
	t.Helper()
	dir = t.TempDir()
	mustRun(t, dir, "init")
	data, err := os.ReadFile("testdata/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "assay", "release.yaml"), data, 0644)
	mustRun(t, dir, "contract", "publish", "assay/release.yaml", "--yes")
	os.WriteFile(filepath.Join(dir, "README.md"), nil, 0644)
	stdout, _, _ := runIn(dir, false, "check", "release", "--json")
	var checked struct {
		Run struct {
			ShortID string `json:"shortId"`
		} `json:"run"`
	}
	if err := json.Unmarshal([]byte(stdout), &checked); err != nil {
		t.Fatal(err)
	}
	return dir, checked.Run.ShortID
}

func TestGraphFormats(t *testing.T) {
	dir, run := releaseProject(t)
	for _, tc := range []struct {
		golden string
		args   []string
	}{
		{"graph-file.txt", []string{"contract", "graph", "assay/release.yaml"}},
		{"graph.txt", []string{"contract", "graph", "release"}},
		{"graph-run.txt", []string{"contract", "graph", "release", "--run", run}},
		{"graph.mmd", []string{"contract", "graph", "release@1", "--format", "mermaid"}},
		{"graph-run.mmd", []string{"contract", "graph", "release", "--format", "mermaid", "--run", run}},
		{"graph.dot", []string{"contract", "graph", "release", "--format", "dot"}},
		{"graph-run.dot", []string{"contract", "graph", "release", "--format", "dot", "--run", run}},
		{"graph-run.json", []string{"contract", "graph", "release", "--json", "--run", run}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			golden(t, tc.golden, dir, mustRun(t, dir, tc.args...))
		})
	}
}

func TestGraphMarksRepeatedSubtrees(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "diamond.yaml")
	os.WriteFile(file, []byte(`id: d
name: Diamond
rules:
  - {id: base, criterion: Base, check: file_exists, path: a, depends_on: [root]}
  - {id: root, criterion: Root, check: file_exists, path: b}
  - {id: left, criterion: Left, check: file_exists, path: c, depends_on: [base]}
  - {id: right, criterion: Right, check: file_exists, path: d, depends_on: [base]}
  - {id: top, criterion: Top, check: file_exists, path: e, depends_on: [left, right]}
  - {id: alone, criterion: Alone, check: file_exists, path: f}
`), 0644)
	stdout := mustRun(t, dir, "contract", "graph", file)
	want := file + ` (Diamond, unpublished)
top — Top
├── left — Left
│   └── base — Base
│       └── root — Root
└── right — Right
    └── base (see above)
alone — Alone
`
	if stdout != want {
		t.Fatalf("got:\n%s\nwant:\n%s", stdout, want)
	}
}

func TestGraphErrors(t *testing.T) {
	dir, run := releaseProject(t)
	os.WriteFile(filepath.Join(dir, "assay", "other.yaml"), []byte(strings.ReplaceAll(docsContract, "docs", "other")), 0644)
	mustRun(t, dir, "contract", "publish", "assay/other.yaml", "--yes")
	os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("id: x\n"), 0644)

	stdout, _, code := runIn(dir, false, "contract", "graph", "bad.yaml")
	if code != ExitNegative || !strings.Contains(stdout, "contract name is required") {
		t.Fatalf("invalid file: exit %d\n%s", code, stdout)
	}
	for _, tc := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"contract", "graph"}, ExitUsage, "exactly one contract"},
		{[]string{"contract", "graph", "release", "--format", "svg"}, ExitUsage, `unknown format "svg"`},
		{[]string{"contract", "graph", "release", "--format", "dot", "--json"}, ExitUsage, "can't be combined"},
		{[]string{"contract", "graph", "assay/release.yaml", "--run", run}, ExitUsage, "needs a published contract"},
		{[]string{"contract", "graph", "other", "--run", run}, ExitUsage, "checks release@1, not other"},
		{[]string{"contract", "graph", "release@2", "--run", run}, ExitUsage, "checks release@1, not release@2"},
		{[]string{"contract", "graph", "nope"}, ExitUsage, `no published contract "nope"`},
		{[]string{"contract", "graph", "missing.yaml"}, ExitOperational, "no such file"},
		{[]string{"contract", "graph", "release", "--run", "zzzz"}, ExitUsage, `no run matches "zzzz"`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, stderr, code := runIn(dir, false, tc.args...)
			if code != tc.code || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("exit %d (want %d): %s", code, tc.code, stderr)
			}
		})
	}
}

// TestGraphDotRenders checks the golden dot files with Graphviz when it is
// installed; CI without Graphviz skips it.
func TestGraphDotRenders(t *testing.T) {
	dot, err := exec.LookPath("dot")
	if err != nil {
		t.Skip("Graphviz dot is not installed")
	}
	for _, name := range []string{"graph.dot", "graph-run.dot"} {
		input, err := os.Open(filepath.Join("testdata", "golden", name))
		if err != nil {
			t.Fatal(err)
		}
		render := exec.Command(dot, "-Tsvg")
		render.Stdin = input
		out, err := render.CombinedOutput()
		input.Close()
		if err != nil || !strings.Contains(string(out), "<svg") {
			t.Errorf("%s: dot failed: %v\n%s", name, err, out)
		}
	}
}
