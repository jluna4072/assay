package storage

import (
	"errors"
	"harness/backend"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func contract() backend.Contract {
	return backend.Contract{ID: "docs", Name: "Documentation", Rules: []backend.Rule{
		{ID: "readme", Criterion: "Project has README", Kind: backend.FileExists, Path: "README.md"},
	}}
}
func openTest(t *testing.T) (*SQLite, *backend.Service) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "harness.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, backend.NewService(store)
}
func publish(t *testing.T, s *backend.Service, c backend.Contract) backend.PublishedContract {
	t.Helper()
	if _, err := s.SaveDraft(c); err != nil {
		t.Fatal(err)
	}
	v, err := s.PublishContract(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFailedThenPassedAttemptSurvivesReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data", "harness.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	service := backend.NewService(store)
	c := contract()
	publish(t, service, c)
	project := t.TempDir()
	run, err := service.StartRun(c.ID, 1, project)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := service.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "failed" || len(failed.Attempts) != 1 || failed.Attempts[0].Results[0].Reason == "" {
		t.Fatalf("failed attempt: %+v", failed)
	}
	first := failed.Attempts[0]
	c.Rules[0].Path = "NEW.md"
	publish(t, service, c)
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	passed, err := service.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if passed.Status != "passed" || passed.Version != 1 || len(passed.Attempts) != 2 || passed.Attempts[1].Number != 2 {
		t.Fatalf("recheck: %+v", passed)
	}
	if !reflect.DeepEqual(first, passed.Attempts[0]) {
		t.Fatal("earlier failure changed")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := backend.NewService(store).GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(passed, reloaded) {
		t.Fatalf("history lost after restart: %+v", reloaded)
	}
	old, _ := store.GetVersion(c.ID, 1)
	current, _ := store.GetDraft(c.ID)
	if old.Contract.Rules[0].Path != "README.md" || current.Rules[0].Path != "NEW.md" {
		t.Fatal("version isolation lost")
	}
}

func TestDependencyFailuresAndOperationalErrors(t *testing.T) {
	_, s := openTest(t)
	c := contract()
	c.Rules = append([]backend.Rule{{ID: "guide", Criterion: "Guide exists", Kind: backend.FileExists, Path: "guide.md", DependsOn: []string{"readme"}}}, c.Rules...)
	publish(t, s, c)
	project := t.TempDir()
	run, err := s.StartRun(c.ID, 1, project)
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Attempts[0].Results[0].Status != backend.Blocked || run.Attempts[0].Results[1].Status != backend.Failed {
		t.Fatalf("dependencies: %+v", run)
	}
	if err := os.RemoveAll(project); err != nil {
		t.Fatal(err)
	}
	run, err = s.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "error" || run.Attempts[1].Results[1].Status != backend.OperationalError {
		t.Fatalf("operational error: %+v", run)
	}
	if run.Attempts[1].Results[1].Passed {
		t.Fatal("error produced a pass")
	}
}

func TestAtomicAttemptRollbackAndForeignKeys(t *testing.T) {
	store, s := openTest(t)
	c := contract()
	publish(t, s, c)
	run, err := s.StartRun(c.ID, 1, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result := backend.RuleResult{RuleID: "same", Status: backend.Failed}
	bad := backend.Attempt{StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), Status: backend.Failed, Results: []backend.RuleResult{result, result}}
	if _, err := store.AppendAttempt(run.ID, bad); err == nil {
		t.Fatal("duplicate results should fail transaction")
	}
	got, err := store.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ready" || len(got.Attempts) != 0 {
		t.Fatal("partially saved attempt")
	}
	run, err = s.EvaluateAttempt(run.ID)
	if err != nil || run.Attempts[0].Number != 1 {
		t.Fatalf("attempt allocation after rollback: %+v %v", run, err)
	}
	if _, err := s.StartRun(c.ID, 99, t.TempDir()); !errors.Is(err, backend.ErrNotFound) {
		t.Fatal("unknown version accepted", err)
	}
	if _, err := s.StartRun(c.ID, 1, ""); err == nil {
		t.Fatal("empty project accepted")
	}
	if err := store.CreateRun(backend.Run{ID: "invalid", ContractID: c.ID, Version: 99}); err == nil {
		t.Fatal("foreign key failed to protect run version")
	}
	if _, err := store.db.Exec("UPDATE versions SET payload='{}' WHERE contract_id=?", c.ID); err == nil {
		t.Fatal("published version updated")
	}
	if _, err := store.db.Exec("DELETE FROM versions WHERE contract_id=?", c.ID); err == nil {
		t.Fatal("published version deleted")
	}
}

func TestConcurrentRechecksAllocateDistinctAttempts(t *testing.T) {
	_, s := openTest(t)
	c := contract()
	publish(t, s, c)
	run, err := s.StartRun(c.ID, 1, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.EvaluateAttempt(run.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	run, err = s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Attempts) != 4 {
		t.Fatal("lost attempt")
	}
	for i, a := range run.Attempts {
		if a.Number != i+1 {
			t.Fatal("duplicate attempt numbers")
		}
	}
}

func TestConcurrentRunReadsKeepStatusAndHistoryConsistent(t *testing.T) {
	store, s := openTest(t)
	c := contract()
	publish(t, s, c)
	run, err := s.StartRun(c.ID, 1, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			status := backend.Failed
			if i%2 == 0 {
				status = backend.Passed
			}
			at := time.Now().UTC()
			if _, err := store.AppendAttempt(run.ID, backend.Attempt{StartedAt: at, CompletedAt: at, Status: status, Results: []backend.RuleResult{{RuleID: "readme", Status: status, Passed: status == backend.Passed}}}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for range 100 {
		snapshot, err := store.GetRun(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Attempts) > 0 && snapshot.Status != string(snapshot.Attempts[len(snapshot.Attempts)-1].Status) {
			t.Error("run status and history came from different snapshots")
		}
	}
	wg.Wait()
}
