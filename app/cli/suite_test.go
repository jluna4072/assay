package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// suiteLibraryWithMCP makes a suite library holding an "mcp" suite of two
// contracts, docs depending on server.
func suiteLibraryWithMCP(t *testing.T) string {
	t.Helper()
	library := t.TempDir()
	t.Setenv("ASSAY_SUITES", library)
	dir := filepath.Join(library, "mcp")
	os.MkdirAll(dir, 0755)
	files := map[string]string{
		"suite.yaml":  "version: 1\nid: mcp\nname: Ready-to-ship MCP tool\ncontracts: [server, docs]\ndepends_on:\n  docs: [server]\n",
		"server.yaml": "id: server\nname: Server\nrules:\n  - {id: tested, criterion: Tests pass, check: command_succeeds, command: test}\n",
		"docs.yaml":   "id: docs\nname: Docs\nrules:\n  - {id: readme, criterion: Has a README, check: file_exists, path: README.md}\n",
		"notes.txt":   "not part of the suite",
	}
	for name, content := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
	}
	return library
}

func TestSuites(t *testing.T) {
	library := suiteLibraryWithMCP(t)
	out := mustRun(t, t.TempDir(), "suite", "list")
	if !strings.Contains(out, "mcp  Ready-to-ship MCP tool · 2 contracts (server, docs) · 2 rules") {
		t.Fatalf("suite list:\n%s", out)
	}

	dir := t.TempDir()
	mustRun(t, dir, "init")
	// The project hasn't approved "test" yet: added, but not valid here.
	stdout, _, code := runIn(dir, false, "suite", "add", "mcp")
	if code != ExitNegative || !strings.Contains(stdout, `assay/suites/mcp/server.yaml:4:`) || !strings.Contains(stdout, `command "test" isn't approved`) {
		t.Fatalf("suite add: exit %d\n%s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "assay", "suites", "mcp", "notes.txt")); err == nil {
		t.Error("copied a file the suite doesn't list")
	}
	if _, stderr, code := runIn(dir, false, "suite", "add", "mcp"); code != ExitUsage || !strings.Contains(stderr, "--force") {
		t.Fatalf("adding twice: exit %d %s", code, stderr)
	}

	config := filepath.Join(dir, ".assay", "config.yaml")
	os.WriteFile(config, []byte("version: 1\nsandbox: off\ncommands:\n  test:\n    run: [sh, -c, \"test -f TESTED\"]\n"), 0600)
	stdout = mustRun(t, dir, "suite", "add", "--force", filepath.Join(library, "mcp"))
	if !strings.Contains(stdout, "✓ assay/suites/mcp: mcp, 2 contracts, 2 rules") {
		t.Fatalf("suite add --force from a folder:\n%s", stdout)
	}
	mustRun(t, dir, "contract", "validate", "assay/suites/mcp")
	mustRun(t, dir, "contract", "publish", "assay/suites/mcp", "--yes")
	if out := mustRun(t, dir, "contract", "list"); !strings.Contains(out, "assay/suites/mcp    mcp · suite of 2 contracts, 2 rules · published as v1") {
		t.Fatalf("contract list:\n%s", out)
	}

	stdout, _, code = runIn(dir, false, "check", "mcp")
	if code != ExitNegative || !strings.Contains(stdout, "✗ server/tested") || !strings.Contains(stdout, "⊘ docs/readme     blocked: dependency server/tested did not pass") {
		t.Fatalf("check: exit %d\n%s", code, stdout)
	}
	os.WriteFile(filepath.Join(dir, "TESTED"), nil, 0644)
	os.WriteFile(filepath.Join(dir, "README.md"), nil, 0644)
	if stdout = mustRun(t, dir, "recheck"); !strings.Contains(stdout, "all 2 rules passed") {
		t.Fatalf("recheck:\n%s", stdout)
	}

	// Agents see suites through MCP like any contract.
	s := mcpClient(t, dir)
	text, isErr := call(t, s, "validate_contract", map[string]any{"path": "assay/suites/mcp"})
	if isErr || !strings.Contains(text, `"valid": true`) {
		t.Fatalf("validate_contract: %s", text)
	}

	if _, stderr, code := runIn(dir, false, "suite", "add", "nope"); code != ExitUsage || !strings.Contains(stderr, "isn't a suite") {
		t.Fatalf("unknown suite: exit %d %s", code, stderr)
	}
}
