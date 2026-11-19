package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestBinaryExitCodes builds the real binary and checks that cli.Run's exit
// codes reach the process.
func TestBinaryExitCodes(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "harness")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(good, []byte("id: a\nname: A\nrules:\n  - id: r\n    criterion: c\n    check: file_exists\n    path: README.md\n"), 0600)
	os.WriteFile(bad, []byte("id: a\n"), 0600)

	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"contract", "validate", good}, 0},
		{[]string{"contract", "validate", bad}, 1},
		{[]string{"contract", "validate", filepath.Join(dir, "missing.yaml")}, 2},
		{[]string{"nope"}, 64},
		{[]string{"init"}, 0},
		// The test's stdin is not a terminal, so publishing needs --yes.
		{[]string{"contract", "publish", good}, 64},
		{[]string{"contract", "publish", good, "--yes"}, 0},
		{[]string{"contract", "show", "a@1"}, 0},
	} {
		command := exec.Command(binary, tc.args...)
		command.Dir = dir
		err := command.Run()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tc.code {
			t.Errorf("%v: exit %d, want %d", tc.args, code, tc.code)
		}
	}
}
