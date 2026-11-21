package contractfile

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"assay/backend"
)

func writeSuite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const (
	serverContract = `id: server
name: Server
rules:
  - {id: builds, criterion: It builds, check: command_succeeds, command: build}
  - {id: tests, criterion: Tests pass, check: command_succeeds, command: test, depends_on: [builds]}
`
	docsContract = `id: docs
name: Docs
rules:
  - {id: readme, criterion: Has a README, check: file_exists, path: README.md}
`
)

func TestSuiteCombinesContracts(t *testing.T) {
	dir := writeSuite(t, map[string]string{
		"suite.yaml":  "version: 1\nid: mcp\nname: MCP tool\ncontracts: [server, docs]\ndepends_on:\n  docs: [server]\n",
		"server.yaml": serverContract,
		"docs.yaml":   docsContract,
	})
	for _, path := range []string{dir, filepath.Join(dir, SuiteFile)} {
		result, err := Check(path)
		if err != nil || !result.Valid() {
			t.Fatalf("Check(%s): %v %v", path, err, result.Problems)
		}
		c := result.Contract
		if c.ID != "mcp" || c.Name != "MCP tool" || !slices.Equal(result.Members, []string{"server", "docs"}) {
			t.Fatalf("combined %+v, members %v", c, result.Members)
		}
		var ids []string
		for _, rule := range c.Rules {
			ids = append(ids, rule.ID)
		}
		if !slices.Equal(ids, []string{"server/builds", "server/tests", "docs/readme"}) {
			t.Fatalf("rules %v", ids)
		}
		if !slices.Equal(c.Rules[1].DependsOn, []string{"server/builds"}) || !slices.Equal(c.Rules[2].DependsOn, []string{"server/builds", "server/tests"}) {
			t.Fatalf("dependencies %v %v", c.Rules[1].DependsOn, c.Rules[2].DependsOn)
		}
	}
	if !IsSuite(dir) || IsSuite(t.TempDir()) {
		t.Error("IsSuite")
	}

	// Problems found in the combined contract point into the member file.
	result, _ := Check(dir)
	result.AddValidation(backend.ValidateCommands(result.Contract, backend.Config{Commands: map[string]backend.CommandSpec{"build": {}}}))
	if len(result.Problems) != 1 {
		t.Fatalf("problems %v", result.Problems)
	}
	p := result.Problems[0]
	if p.File != "server.yaml" || p.Line != 5 || p.RuleID != "tests" || !strings.Contains(p.Message, `command "test" isn't approved`) {
		t.Fatalf("problem %+v", p)
	}
	if got := p.In("assay/suites/mcp"); !strings.HasPrefix(got, "assay/suites/mcp/server.yaml:5:") {
		t.Errorf("In: %s", got)
	}
}

func TestSuiteProblems(t *testing.T) {
	dir := writeSuite(t, map[string]string{
		"suite.yaml":  "id: mcp\nname: MCP\ncontracts: [server, docs, missing, server, ../x]\ndepends_on:\n  docs: [nope]\n  other: [server]\n",
		"server.yaml": strings.Replace(serverContract, "id: server", "id: srv", 1),
		"docs.yaml":   strings.Replace(docsContract, "criterion: Has a README, ", "", 1),
	})
	result, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range result.Problems {
		got = append(got, p.In("s"))
	}
	text := strings.Join(got, "\n")
	for _, want := range []string{
		`s/docs.yaml:4:5: rule "readme": acceptance criterion is required`,
		`s/server.yaml:1:1: in a suite, a contract's id must match its file name: "server"`,
		`s/suite.yaml:3:27: there's no missing.yaml in the suite's folder`,
		`s/suite.yaml:3:36: server is listed twice`,
		`s/suite.yaml:3:44: "../x" isn't a contract file name`,
		`s/suite.yaml:5:3: docs depends on nope, which isn't in contracts`,
		`s/suite.yaml:6:3: depends_on names other, which isn't in contracts`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q in:\n%s", want, text)
		}
	}

	cycle := writeSuite(t, map[string]string{
		"suite.yaml":  "id: s\nname: S\ncontracts: [server, docs]\ndepends_on: {server: [docs], docs: [server]}\n",
		"server.yaml": serverContract,
		"docs.yaml":   docsContract,
	})
	result, _ = Check(cycle)
	if len(result.Problems) != 1 || !strings.Contains(result.Problems[0].Message, "dependency cycle server → docs → server") {
		t.Fatalf("cycle: %v", result.Problems)
	}

	strict := writeSuite(t, map[string]string{"suite.yaml": "id: s\nname: S\ncontract: [a]\n"})
	result, _ = Check(strict)
	if len(result.Problems) == 0 || !strings.Contains(result.Problems[0].String(), `unknown key "contract"`) {
		t.Fatalf("unknown key: %v", result.Problems)
	}

	if _, err := Check(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a folder without suite.yaml: %v", err)
	}
}
