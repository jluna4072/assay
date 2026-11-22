package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// assay runs git outside the sandbox, in the project and in work runs'
// workspaces, which a worker controls. git runs programs named in its
// config (core.fsmonitor, hooks, diff drivers), and finds its config
// through the worktree's .git, which the worker can rewrite. So git never
// trusts a workspace's .git: its git directory is looked up in the
// project's own repository and passed explicitly, and features that run
// programs are turned off for every call.

// gitSafety turns off git features that run programs from config.
var gitSafety = []string{
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.untrackedCache=false",
}

// git runs a git command in dir with a timeout and returns its stdout.
// GIT_* variables in assay's own environment are dropped, so they can't
// redirect it; env adds assay's own.
func git(dir string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, gitSafety...), args...)...)
	cmd.Dir = dir
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", subcommand(args), message)
	}
	return stdout.Bytes(), nil
}

// workspaceGitDir finds the git directory of a work run's workspace in the
// project's repository (<common dir>/worktrees/<name>), ignoring the
// workspace's own .git file.
func workspaceGitDir(root, workspace string) (string, error) {
	out, err := git(root, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(string(out))
	want, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	for _, entry := range entries {
		dir := filepath.Join(common, "worktrees", entry.Name())
		pointer, err := os.ReadFile(filepath.Join(dir, "gitdir"))
		if err != nil {
			continue
		}
		path := strings.TrimSpace(string(pointer))
		if filepath.Base(path) != ".git" {
			continue
		}
		if got, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil && got == want {
			return dir, nil
		}
	}
	return "", fmt.Errorf("%s isn't a worktree of the project's repository", workspace)
}

// workspaceEnv is the environment that points git at a workspace through
// the project's repository. For the project itself, it is empty.
func workspaceEnv(root, dir string) ([]string, error) {
	if root == "" || samePath(root, dir) {
		return nil, nil
	}
	gitDir, err := workspaceGitDir(root, dir)
	if err != nil {
		return nil, err
	}
	return []string{"GIT_DIR=" + gitDir, "GIT_WORK_TREE=" + dir}, nil
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// subcommand names a git command for errors, skipping "-c name=value".
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}
