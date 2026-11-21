package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"assay/contractfile"
)

// runIn runs assay in dir. interactive simulates a person at a terminal.
func runIn(dir string, interactive bool, args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = Run(args, Env{Stdout: &out, Stderr: &errOut, Dir: dir, Interactive: interactive})
	return out.String(), errOut.String(), code
}

func mustRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, code := runIn(dir, false, args...)
	if code != ExitOK {
		t.Fatalf("assay %s: exit %d\n%s%s", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

func TestInitIsIdempotentAndIgnoresState(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules"), 0644)

	out := mustRun(t, dir, "init")
	for _, want := range []string{"created  .assay/assay.db", "created  .assay/config.yaml", "created  assay/example.yaml", "added    /.assay/ to .gitignore", "Next: assay contract validate assay/example.yaml"} {
		if !strings.Contains(out, want) {
			t.Errorf("first init output lacks %q:\n%s", want, out)
		}
	}
	ignore, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(ignore) != "node_modules\n/.assay/\n" {
		t.Errorf(".gitignore: %q", ignore)
	}
	if result, err := contractfile.Check(filepath.Join(dir, "assay", "example.yaml")); err != nil || !result.Valid() {
		t.Errorf("example contract is not valid: %v %v", err, result.Problems)
	}

	os.WriteFile(filepath.Join(dir, "assay", "example.yaml"), []byte("edited"), 0644)
	out = mustRun(t, dir, "init")
	if !strings.Contains(out, "already initialized") || strings.Contains(out, "created") || strings.Contains(out, "added") {
		t.Errorf("second init changed something:\n%s", out)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "assay", "example.yaml")); string(data) != "edited" {
		t.Error("second init overwrote a contract file")
	}
	if ignore2, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); !bytes.Equal(ignore, ignore2) {
		t.Errorf(".gitignore changed again: %q", ignore2)
	}
}

func TestInitOutsideGitAndWithExistingContracts(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "assay"), 0755)
	os.WriteFile(filepath.Join(dir, "assay", "docs.yaml"), []byte("id: docs\n"), 0644)

	stdout, _, code := runIn(dir, false, "init", "--json")
	var got initJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || code != ExitOK {
		t.Fatalf("exit %d: %v\n%s", code, err, stdout)
	}
	if got.Gitignore != "not a git repository" || len(got.Created) != 2 || got.Created[1] != ".assay/config.yaml" || got.Existing[0] != "assay/" {
		t.Fatalf("unexpected: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Error(".gitignore created outside a git repository")
	}
	if _, err := os.Stat(filepath.Join(dir, "assay", "example.yaml")); !os.IsNotExist(err) {
		t.Error("example written next to existing contracts")
	}
}

func TestPublishVersionsAndShow(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init")
	file := filepath.Join(dir, "assay", "example.yaml")

	// Not at a terminal and no --yes: refused, nothing stored.
	_, stderr, code := runIn(dir, false, "contract", "publish", file)
	if code != ExitUsage || !strings.Contains(stderr, "needs confirmation") {
		t.Fatalf("unconfirmed publish: exit %d %s", code, stderr)
	}
	if _, stderr, code := runIn(dir, false, "contract", "versions", "example"); code != ExitUsage || !strings.Contains(stderr, `no published contract "example"`) {
		t.Fatalf("versions before publishing: exit %d %s", code, stderr)
	}

	// A person at a terminal publishes without --yes.
	stdout, _, code := runIn(dir, true, "contract", "publish", "assay/example.yaml")
	if code != ExitOK || stdout != "published example@1 (1 rule)\n" {
		t.Fatalf("publish: exit %d %q", code, stdout)
	}
	if out := mustRun(t, dir, "contract", "publish", file, "--yes"); out != "example@1 unchanged: the file matches the latest published version\n" {
		t.Fatalf("unchanged publish: %q", out)
	}

	original, _ := os.ReadFile(file)
	os.WriteFile(file, bytes.Replace(original, []byte("README.md"), []byte("README.txt"), 1), 0644)
	sub := filepath.Join(dir, "nested", "deeper")
	os.MkdirAll(sub, 0755)
	if out := mustRun(t, sub, "contract", "publish", file, "--yes"); out != "published example@2 (1 rule)\n" {
		t.Fatalf("publish from a subdirectory: %q", out)
	}

	versions := mustRun(t, dir, "contract", "versions", "example")
	if !strings.HasPrefix(versions, "example (Example contract)\n  @2 ") || !strings.Contains(versions, "latest\n  @1 ") {
		t.Fatalf("versions:\n%s", versions)
	}

	// show prints a valid contract file; @1 still has the original path.
	shown := mustRun(t, dir, "contract", "show", "example@1")
	if !strings.HasPrefix(shown, "# example@1, published ") {
		t.Fatalf("show header:\n%s", shown)
	}
	result := contractfile.Parse([]byte(shown))
	if !result.Valid() || result.Contract.Rules[0].Path != "README.md" {
		t.Fatalf("show @1 is not the original contract: %+v %v", result.Contract, result.Problems)
	}
	if latest := mustRun(t, dir, "contract", "show", "example", "--json"); !strings.Contains(latest, `"version": 2`) || !strings.Contains(latest, `"latest": true`) || !strings.Contains(latest, "README.txt") {
		t.Fatalf("show latest json:\n%s", latest)
	}
}

func TestPublishInvalidAndProjectErrors(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init")
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("id: docs\nname: Docs\nrules: []\n"), 0644)

	stdout, _, code := runIn(dir, true, "contract", "publish", bad)
	if code != ExitNegative || !strings.Contains(stdout, "a contract needs at least one rule") || !strings.Contains(stdout, "nothing published") {
		t.Fatalf("invalid publish: exit %d\n%s", code, stdout)
	}

	elsewhere := t.TempDir()
	for _, tc := range []struct {
		dir    string
		args   []string
		code   int
		stderr string
	}{
		{elsewhere, []string{"contract", "versions", "docs"}, ExitUsage, `Run "assay init"`},
		{elsewhere, []string{"contract", "versions", "docs", "--project", dir}, ExitUsage, `no published contract "docs"`},
		{dir, []string{"contract", "versions", "docs", "--project", elsewhere}, ExitUsage, "no assay project found in " + elsewhere},
		{dir, []string{"contract", "publish", filepath.Join(dir, "missing.yaml"), "--yes"}, ExitOperational, "no such file"},
		{dir, []string{"contract", "publish", "--yes"}, ExitUsage, "exactly one contract file"},
		{dir, []string{"contract", "show", "@2"}, ExitUsage, "missing contract ID"},
		{dir, []string{"contract", "show", "docs@0"}, ExitUsage, "positive number"},
		{dir, []string{"init", "a", "b"}, ExitUsage, "at most one directory"},
		{dir, []string{"init", bad}, ExitOperational, "not a directory"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, stderr, code := runIn(tc.dir, false, tc.args...)
			if code != tc.code || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("exit %d (want %d): %s", code, tc.code, stderr)
			}
		})
	}
}
