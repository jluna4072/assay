package main

import (
	"harness/backend"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopBoundaryRetainsFailureAfterCorrection(t *testing.T) {
	app, err := NewDesktop(filepath.Join(t.TempDir(), "harness.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer app.store.Close()
	c := backend.Contract{ID: "docs", Name: "Docs", Rules: []backend.Rule{{ID: "readme", Criterion: "README exists", Kind: backend.FileExists, Path: "README.md"}}}
	if _, err := app.SaveDraft(c); err != nil {
		t.Fatal(err)
	}
	if _, err := app.PublishContract(c.ID); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	run, err := app.StartRun(c.ID, 1, project)
	if err != nil {
		t.Fatal(err)
	}
	run, err = app.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" {
		t.Fatal("missing file should fail")
	}
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	run, err = app.EvaluateAttempt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "passed" || len(run.Attempts) != 2 || run.Attempts[0].Status != backend.Failed {
		t.Fatalf("history: %+v", run)
	}
	if _, err := app.SelectProject(); err == nil {
		t.Fatal("dialog without window must error")
	}
}
