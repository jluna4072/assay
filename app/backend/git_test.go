package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// workspaceWithForgedGit makes a project with a work run's worktree, then
// does what a hostile worker can: points the workspace's .git at a git
// directory it wrote, whose config runs a program whenever git looks at
// the worktree. It returns the project, the workspace, and the file that
// program creates.
func workspaceWithForgedGit(t *testing.T) (root, workspace, marker string) {
	t.Helper()
	root = t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "t@example.com")
	runGit(t, root, "config", "user.name", "T")
	os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0644)
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "start")
	workspace = filepath.Join(root, ".assay", "workspaces", "run1")
	os.MkdirAll(filepath.Dir(workspace), 0700)
	runGit(t, root, "worktree", "add", "--quiet", "-b", "assay/run1", workspace, "HEAD")

	marker = filepath.Join(t.TempDir(), "escaped")
	evil := filepath.Join(workspace, "evil")
	runGit(t, workspace, "init", "-q", "--separate-git-dir", filepath.Join(evil, "gitdir"), filepath.Join(evil, "tree"))
	hook := filepath.Join(evil, "hook.sh")
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\necho 2\n"), 0755)
	runGit(t, filepath.Join(evil, "tree"), "config", "core.fsmonitor", hook)
	runGit(t, filepath.Join(evil, "tree"), "config", "core.hooksPath", evil)
	os.WriteFile(filepath.Join(evil, "post-commit"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0755)
	os.RemoveAll(filepath.Join(evil, "tree"))
	os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: "+filepath.Join(evil, "gitdir")+"\n"), 0644)
	os.WriteFile(filepath.Join(workspace, "README.md"), []byte("changed\n"), 0644)

	// The forged repository really does run the program.
	if out, err := exec.Command("git", "-C", workspace, "status", "--short").CombinedOutput(); err != nil {
		t.Fatalf("forged repo: %v %s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Skip("this git doesn't run core.fsmonitor; nothing to prove")
	}
	os.Remove(marker)
	return root, workspace, marker
}

func TestCommitIgnoresForgedWorkspaceGit(t *testing.T) {
	root, workspace, marker := workspaceWithForgedGit(t)
	commit, err := commitWorkspace(root, workspace, "RUN1", 1)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("committing the workspace ran a program the worker configured")
	}
	// The commit went to the run's own branch in the project's repository.
	if branch := strings.TrimSpace(runGit(t, root, "rev-parse", "assay/run1")); branch != commit {
		t.Fatalf("assay/run1 is %s, commit %s", branch, commit)
	}
	if show := runGit(t, root, "show", commit+":README.md"); show != "changed\n" {
		t.Fatalf("committed README: %q", show)
	}
}

func TestDiffEvidenceIgnoresForgedWorkspaceGit(t *testing.T) {
	root, workspace, marker := workspaceWithForgedGit(t)
	baseline := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	c := newCollector(workspace, root, baseline, nil)
	item := c.diff([]string{"README.md"})
	if item.Error != "" {
		t.Fatalf("diff: %s", item.Error)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("collecting diff evidence ran a program the worker configured")
	}
	if diff := string(c.blobs[item.Blob]); !strings.Contains(diff, "+changed") {
		t.Fatalf("diff:\n%s", diff)
	}
}

func TestWorkspaceGitDirRejectsUnknownWorkspace(t *testing.T) {
	root, workspace, _ := workspaceWithForgedGit(t)
	if _, err := workspaceGitDir(root, filepath.Join(workspace, "evil", "tree")); err == nil {
		t.Fatal("accepted a folder that isn't one of the project's worktrees")
	}
	if dir, err := workspaceGitDir(root, workspace); err != nil || !strings.Contains(dir, filepath.Join(".git", "worktrees")) {
		t.Fatalf("workspace git dir: %q %v", dir, err)
	}
}

func TestGitIgnoresInheritedGitVariables(t *testing.T) {
	root, _, _ := workspaceWithForgedGit(t)
	t.Setenv("GIT_DIR", "/nonexistent")
	if Baseline(root) == "" {
		t.Fatal("GIT_DIR from assay's environment redirected its git")
	}
}
