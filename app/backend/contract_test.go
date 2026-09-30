package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFileExists(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "present.txt"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(projectDir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		path   string
		passed bool
	}{
		{name: "file", path: "present.txt", passed: true},
		{name: "missing", path: "missing.txt", passed: false},
		{name: "directory", path: "folder", passed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := CheckFileExists(projectDir, Rule{ID: "file-rule", Path: tc.path})
			if err != nil {
				t.Fatal(err)
			}
			if result.RuleID != "file-rule" || result.Passed != tc.passed {
				t.Fatalf("unexpected result: %+v", result)
			}
			if !result.Passed && result.Reason == "" {
				t.Fatal("failed rule needs a reason")
			}
		})
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projectDir, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckFileExists(projectDir, Rule{ID: "file-rule", Path: "escape.txt"}); err == nil {
		t.Fatal("symlink outside the project must not be checked")
	}
}
