package storage

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness/backend"

	_ "modernc.org/sqlite"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "commit"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func evidenceContract(specs ...backend.EvidenceSpec) backend.Contract {
	return backend.Contract{ID: "docs", Name: "Docs", Rules: []backend.Rule{
		{ID: "readme", Criterion: "README exists", Kind: backend.FileExists, Path: "README.md", Evidence: specs},
	}}
}

func TestFileEvidenceIsAFrozenSnapshot(t *testing.T) {
	_, s := openTest(t)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "README.md"), []byte("first version"), 0644)
	publish(t, s, evidenceContract(
		backend.EvidenceSpec{Kind: backend.FileEvidence, Path: "README.md"},
		backend.EvidenceSpec{Kind: backend.FileEvidence, Path: "missing.md"},
	))
	run, err := s.StartRun("docs", 1, project)
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := run.Attempts[0].Results[0]
	if result.Status != backend.Passed || len(result.Evidence) != 2 {
		t.Fatalf("result: %+v", result)
	}
	readme, missing := result.Evidence[0], result.Evidence[1]
	if readme.Size != 13 || readme.Truncated || readme.Blob == "" || readme.SHA256 != readme.Blob {
		t.Fatalf("readme evidence: %+v", readme)
	}
	if !missing.Absent || missing.Error != "" || missing.Blob != "" {
		t.Fatalf("absent file evidence: %+v", missing)
	}

	// Changing the file and checking again leaves attempt 1's evidence alone.
	os.WriteFile(filepath.Join(project, "README.md"), []byte("second version"), 0644)
	run, err = s.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.GetEvidence(run.Attempts[0].Results[0].Evidence[0].Blob)
	second, _ := s.GetEvidence(run.Attempts[1].Results[0].Evidence[0].Blob)
	if string(first) != "first version" || string(second) != "second version" {
		t.Fatalf("evidence: %q then %q", first, second)
	}
	if _, err := s.GetEvidence("nope"); err != backend.ErrNotFound {
		t.Fatalf("unknown blob: %v", err)
	}
}

func TestEvidenceErrorsPreventAPass(t *testing.T) {
	_, s := openTest(t)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "README.md"), nil, 0644)
	os.Mkdir(filepath.Join(project, "docs"), 0755)
	os.Symlink(filepath.Join(t.TempDir(), "outside.txt"), filepath.Join(project, "escape.txt"))
	for _, tc := range []struct {
		spec   backend.EvidenceSpec
		reason string
	}{
		{backend.EvidenceSpec{Kind: backend.FileEvidence, Path: "docs"}, "docs is not a regular file"},
		{backend.EvidenceSpec{Kind: backend.FileEvidence, Path: "escape.txt"}, "file evidence couldn't be collected"},
		{backend.EvidenceSpec{Kind: backend.DiffEvidence}, "isn't a git repository"},
	} {
		c := evidenceContract(tc.spec)
		c.ID = strings.ReplaceAll(tc.spec.Path+string(tc.spec.Kind), ".", "-")
		publish(t, s, c)
		run, err := s.StartRun(c.ID, 1, project)
		if err != nil {
			t.Fatal(err)
		}
		run, err = s.EvaluateAttempt(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		result := run.Attempts[0].Results[0]
		if result.Status != backend.OperationalError || result.Passed || !strings.Contains(result.Reason, tc.reason) || run.Status != "error" {
			t.Errorf("%v: %+v", tc.spec, result)
		}
	}
}

func TestDiffEvidenceIncludesUntrackedFiles(t *testing.T) {
	_, s := openTest(t)
	project := gitRepo(t)
	os.WriteFile(filepath.Join(project, "README.md"), []byte("one\n"), 0644)
	os.WriteFile(filepath.Join(project, "old.txt"), []byte("old\n"), 0644)
	os.WriteFile(filepath.Join(project, ".gitignore"), []byte(".harness/\nignored.txt\n"), 0644)
	commitAll(t, project)
	publish(t, s, evidenceContract(
		backend.EvidenceSpec{Kind: backend.DiffEvidence},
		backend.EvidenceSpec{Kind: backend.DiffEvidence, Paths: []string{"docs"}},
	))
	run, err := s.StartRun("docs", 1, project)
	if err != nil || len(run.Baseline) != 40 {
		t.Fatalf("baseline: %q %v", run.Baseline, err)
	}

	os.WriteFile(filepath.Join(project, "README.md"), []byte("two\n"), 0644)
	os.Remove(filepath.Join(project, "old.txt"))
	os.MkdirAll(filepath.Join(project, "docs"), 0755)
	os.WriteFile(filepath.Join(project, "docs", "new.md"), []byte("new\n"), 0644)
	os.WriteFile(filepath.Join(project, "ignored.txt"), []byte("secret\n"), 0644)
	exec.Command("git", "-C", project, "add", "README.md").Run() // staged changes are included too
	run, err = s.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	whole, docs := run.Attempts[0].Results[0].Evidence[0], run.Attempts[0].Results[0].Evidence[1]
	if whole.Error != "" || whole.Files != 3 || whole.Baseline != run.Baseline {
		t.Fatalf("whole-project diff: %+v", whole)
	}
	content, _ := s.GetEvidence(whole.Blob)
	for _, want := range []string{"-one", "+two", "deleted file mode", "old.txt", "new file mode", "docs/new.md"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("diff lacks %q:\n%s", want, content)
		}
	}
	if strings.Contains(string(content), "ignored.txt") || strings.Contains(string(content), ".harness") {
		t.Errorf("diff includes ignored files:\n%s", content)
	}
	if docs.Files != 1 {
		t.Errorf("docs-only diff: %+v", docs)
	}
	// The project's own index is untouched: README.md is still the only staged file.
	staged, _ := exec.Command("git", "-C", project, "diff", "--cached", "--name-only").Output()
	if strings.TrimSpace(string(staged)) != "README.md" {
		t.Errorf("project index changed: %q", staged)
	}
	if entries, _ := os.ReadDir(filepath.Join(project, ".harness", "tmp")); len(entries) != 0 {
		t.Errorf("temporary index left behind: %v", entries)
	}
}

func TestDiffEvidenceEdgeCases(t *testing.T) {
	_, s := openTest(t)
	project := gitRepo(t) // no .gitignore: harness's own state must still be left out
	os.MkdirAll(filepath.Join(project, "gone"), 0755)
	os.WriteFile(filepath.Join(project, "gone", "a.txt"), []byte("a\n"), 0644)
	commitAll(t, project)
	os.MkdirAll(filepath.Join(project, ".harness"), 0700)
	os.WriteFile(filepath.Join(project, ".harness", "harness.db"), []byte("state"), 0600)
	publish(t, s, evidenceContract(
		backend.EvidenceSpec{Kind: backend.DiffEvidence},
		backend.EvidenceSpec{Kind: backend.DiffEvidence, Paths: []string{"not-created-yet"}},
		backend.EvidenceSpec{Kind: backend.DiffEvidence, Paths: []string{"gone"}},
	))
	os.WriteFile(filepath.Join(project, "README.md"), nil, 0644)
	run, _ := s.StartRun("docs", 1, project)
	os.RemoveAll(filepath.Join(project, "gone"))
	run, err := s.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	items := run.Attempts[0].Results[0].Evidence
	whole, _ := s.GetEvidence(items[0].Blob)
	if items[0].Error != "" || strings.Contains(string(whole), ".harness") || !strings.Contains(string(whole), "README.md") {
		t.Errorf("whole diff (%+v):\n%s", items[0], whole)
	}
	if items[1].Error != "" || items[1].Files != 0 || items[1].Size != 0 {
		t.Errorf("diff of a path that doesn't exist: %+v", items[1])
	}
	if items[2].Error != "" || items[2].Files != 1 {
		t.Errorf("diff of a deleted directory: %+v", items[2])
	}
	if run.Attempts[0].Status != backend.Passed {
		t.Errorf("attempt: %+v", run.Attempts[0])
	}
}

func TestBaselineOfEmptyRepository(t *testing.T) {
	project := gitRepo(t)
	if got := backend.Baseline(project); got != backend.EmptyTree {
		t.Fatalf("baseline %q", got)
	}
	if got := backend.Baseline(t.TempDir()); got != "" {
		t.Fatalf("non-repository baseline %q", got)
	}
}

func TestLargeEvidenceIsTruncated(t *testing.T) {
	_, s := openTest(t)
	project := t.TempDir()
	big := strings.Repeat("x", backend.MaxEvidenceBytes+10)
	os.WriteFile(filepath.Join(project, "README.md"), []byte(big), 0644)
	publish(t, s, evidenceContract(backend.EvidenceSpec{Kind: backend.FileEvidence, Path: "README.md"}))
	run, _ := s.StartRun("docs", 1, project)
	run, err := s.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := run.Attempts[0].Results[0].Evidence[0]
	stored, _ := s.GetEvidence(item.Blob)
	if !item.Truncated || item.Size != int64(len(big)) || len(stored) != backend.MaxEvidenceBytes || item.SHA256 == item.Blob {
		t.Fatalf("truncation: %+v stored %d", item, len(stored))
	}
}

// TestUpgradeFromSchemaVersion1 opens a database written by the previous
// release and checks its runs survive with an empty baseline.
func TestUpgradeFromSchemaVersion1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE drafts (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE versions (contract_id TEXT NOT NULL REFERENCES drafts(id), version INTEGER NOT NULL, payload TEXT NOT NULL, PRIMARY KEY(contract_id, version));
CREATE TABLE runs (id TEXT PRIMARY KEY, contract_id TEXT NOT NULL, version INTEGER NOT NULL, project_dir TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, FOREIGN KEY(contract_id, version) REFERENCES versions(contract_id, version));
CREATE TABLE attempts (run_id TEXT NOT NULL REFERENCES runs(id), number INTEGER NOT NULL, started_at TEXT NOT NULL, completed_at TEXT NOT NULL, status TEXT NOT NULL, PRIMARY KEY(run_id, number));
CREATE TABLE rule_results (run_id TEXT NOT NULL, attempt_number INTEGER NOT NULL, position INTEGER NOT NULL, rule_id TEXT NOT NULL, payload TEXT NOT NULL, PRIMARY KEY(run_id, attempt_number, position), UNIQUE(run_id, attempt_number, rule_id), FOREIGN KEY(run_id, attempt_number) REFERENCES attempts(run_id, number));
CREATE TRIGGER versions_no_update BEFORE UPDATE ON versions BEGIN SELECT RAISE(ABORT, 'published versions are immutable'); END;
CREATE TRIGGER versions_no_delete BEFORE DELETE ON versions BEGIN SELECT RAISE(ABORT, 'published versions are immutable'); END;
INSERT INTO drafts VALUES ('docs', '{"id":"docs","name":"Docs","rules":[{"id":"readme","criterion":"c","kind":"file_exists","path":"README.md","dependsOn":null}]}');
INSERT INTO versions VALUES ('docs', 1, '{"contract":{"id":"docs","name":"Docs","rules":[{"id":"readme","criterion":"c","kind":"file_exists","path":"README.md","dependsOn":null}]},"version":1,"publishedAt":"2026-11-01T00:00:00Z"}');
INSERT INTO runs VALUES ('OLDRUN', 'docs', 1, '/tmp/project', 'failed', '2026-11-01T00:00:00Z');
INSERT INTO attempts VALUES ('OLDRUN', 1, '2026-11-01T00:00:00Z', '2026-11-01T00:00:01Z', 'failed');
INSERT INTO rule_results VALUES ('OLDRUN', 1, 0, 'readme', '{"ruleId":"readme","passed":false,"reason":"README.md does not exist","status":"failed"}');
PRAGMA user_version = 1;`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRun("OLDRUN")
	if err != nil || run.Baseline != "" || len(run.Attempts) != 1 || run.Attempts[0].Results[0].Reason != "README.md does not exist" {
		t.Fatalf("old run after upgrade: %+v %v", run, err)
	}
	var version int
	store.db.QueryRow("PRAGMA user_version").Scan(&version)
	store.Close()
	if version != SchemaVersion {
		t.Fatalf("schema version %d", version)
	}
	// Reopening an upgraded database is a no-op.
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	newer, _ := sql.Open("sqlite", path)
	newer.Exec("PRAGMA user_version = 99")
	newer.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "upgrade harness") {
		t.Fatalf("newer schema: %v", err)
	}
}
