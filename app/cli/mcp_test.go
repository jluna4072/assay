package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpClient connects a client to the project's server in memory.
func mcpClient(t *testing.T, dir string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	if _, err := newMCPServer(dir).Connect(ctx, serverSide, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// call calls a tool and returns its text and whether it was an error.
func call(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	result, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var parts []string
	for _, c := range result.Content {
		parts = append(parts, c.(*mcp.TextContent).Text)
	}
	return strings.Join(parts, "\n"), result.IsError
}

// firstJSON decodes the JSON a tool returned (its first content item).
func firstJSON(t *testing.T, text string, into any) {
	t.Helper()
	if i := strings.Index(text, "\nassay exited"); i >= 0 {
		text = text[:i]
	}
	if err := json.Unmarshal([]byte(text), into); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, text)
	}
}

func TestMCPTools(t *testing.T) {
	dir := evidenceProject(t)
	s := mcpClient(t, dir)

	if !strings.Contains(s.InitializeResult().Instructions, "You can't publish") {
		t.Errorf("instructions: %q", s.InitializeResult().Instructions)
	}
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if strings.Contains(tool.Name, "publish") {
			t.Errorf("there is a publish tool: %s", tool.Name)
		}
	}
	want := []string{"check", "diff_contract", "get_contract", "get_evidence", "get_run", "list_contracts", "list_runs", "recheck", "validate_contract"}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Errorf("tools %v, want %v", names, want)
	}

	text, isErr := call(t, s, "list_contracts", nil)
	var list struct {
		Files     []listFileJSON     `json:"files"`
		Contracts []listContractJSON `json:"contracts"`
	}
	firstJSON(t, text, &list)
	if isErr || len(list.Contracts) != 1 || list.Contracts[0].ID != "docs" || list.Files[0].Published != "same" {
		t.Fatalf("list_contracts: %s", text)
	}

	// An agent writes a contract and validates it.
	os.WriteFile(filepath.Join(dir, "assay", "draft.yaml"), []byte("id: draft\nname: Draft\nrules:\n  - {id: a, criterion: A, check: file_exists, path: A.md, depends_on: [b]}\n"), 0644)
	text, isErr = call(t, s, "validate_contract", map[string]any{"path": "assay/draft.yaml"})
	if isErr || !strings.Contains(text, `dependency \"b\" does not exist`) || !strings.Contains(text, "assay exited 1") {
		t.Fatalf("validate_contract: %v %s", isErr, text)
	}
	if text, isErr = call(t, s, "validate_contract", map[string]any{"path": "../outside.yaml"}); !isErr || !strings.Contains(text, "outside the project") {
		t.Fatalf("outside path: %v %s", isErr, text)
	}
	text, _ = call(t, s, "diff_contract", map[string]any{"path": "assay/docs.yaml"})
	if !strings.Contains(text, `"changed": false`) {
		t.Fatalf("diff_contract: %s", text)
	}

	// check: guide fails until docs/guide.md exists; recheck passes.
	text, isErr = call(t, s, "check", map[string]any{"contract": "docs"})
	if isErr || !strings.Contains(text, "docs/guide.md does not exist") || !strings.Contains(text, "assay exited 1") {
		t.Fatalf("check: %v %s", isErr, text)
	}
	os.MkdirAll(filepath.Join(dir, "docs"), 0755)
	os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte("# Guide\n"), 0644)
	text, isErr = call(t, s, "recheck", nil)
	var attempt struct {
		Attempt int     `json:"attempt"`
		Run     runJSON `json:"run"`
	}
	firstJSON(t, text, &attempt)
	if isErr || attempt.Attempt != 2 || attempt.Run.Status != "passed" || strings.Contains(text, "assay exited") {
		t.Fatalf("recheck: %s", text)
	}

	text, _ = call(t, s, "get_run", map[string]any{"run": attempt.Run.ShortID, "attempt": 1})
	if !strings.Contains(text, `"number": 1`) || strings.Contains(text, `"number": 2`) {
		t.Fatalf("get_run: %s", text)
	}
	text, isErr = call(t, s, "get_evidence", map[string]any{"rule": "guide", "item": 1})
	if isErr || !strings.Contains(text, `"content": "# Guide\n"`) {
		t.Fatalf("get_evidence: %v %s", isErr, text)
	}
	text, _ = call(t, s, "list_runs", map[string]any{"contract": "docs", "limit": 5})
	if !strings.Contains(text, `"attempts": 2`) {
		t.Fatalf("list_runs: %s", text)
	}

	// Arguments are never read as flags, and failures are tool errors.
	if text, isErr = call(t, s, "get_contract", map[string]any{"contract": "--yes"}); !isErr {
		t.Fatalf("flag as contract: %s", text)
	}
	if text, isErr = call(t, s, "get_contract", map[string]any{"contract": "nope"}); !isErr || !strings.Contains(text, `no published contract "nope"`) {
		t.Fatalf("unknown contract: %s", text)
	}
	if text, isErr = call(t, s, "check", map[string]any{}); !isErr || !strings.Contains(text, "missing properties") {
		t.Fatalf("missing contract: %s", text)
	}
	if text, isErr = call(t, s, "check", map[string]any{"contract": " "}); !isErr || !strings.Contains(text, "contract is required") {
		t.Fatalf("empty contract: %s", text)
	}
}

func TestContractList(t *testing.T) {
	dir := newProject(t)
	os.WriteFile(filepath.Join(dir, "assay", "broken.yaml"), []byte("id: broken\n"), 0644)
	got := mustRun(t, dir, "contract", "list")
	for _, want := range []string{"✗ assay/broken.yaml", "✓ assay/docs.yaml     docs · 2 rules · published as v1", "✓ assay/example.yaml  example · 1 rule · not published", "docs  v1  2 rules  Documentation"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
}

func TestMCPNeedsAProject(t *testing.T) {
	_, stderr, code := runIn(t.TempDir(), false, "mcp")
	if code != ExitUsage || !strings.Contains(stderr, "assay init") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
