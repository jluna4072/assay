package backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EvidenceKind names a built-in evidence collector. Contracts choose from
// these; they can't define new ones.
type EvidenceKind string

const (
	// FileEvidence captures a project file's contents.
	FileEvidence EvidenceKind = "file"
	// DiffEvidence captures the changes since the run's baseline commit,
	// including untracked files.
	DiffEvidence EvidenceKind = "diff"
	// CommandEvidence captures an approved command's output and exit status.
	CommandEvidence EvidenceKind = "command"
	// ReviewRequestEvidence and ReviewResponseEvidence record exactly what
	// a reviewer was sent and what it answered. harness adds them to review
	// rules; contracts can't ask for them.
	ReviewRequestEvidence  EvidenceKind = "review_request"
	ReviewResponseEvidence EvidenceKind = "review_response"
)

// MaxEvidenceBytes caps the stored content of one evidence item. Larger
// content is stored up to the cap and marked truncated; its full size and
// hash are still recorded.
const MaxEvidenceBytes = 1 << 20

// EmptyTree is git's hash for a tree with no files: the baseline of a
// repository that has no commits yet.
const EmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// EvidenceSpec is what a rule asks harness to record when it is checked.
type EvidenceSpec struct {
	Kind    EvidenceKind `json:"kind"`
	Path    string       `json:"path,omitempty"`    // file
	Paths   []string     `json:"paths,omitempty"`   // diff: limit to these paths; empty means the whole project
	Command string       `json:"command,omitempty"` // command: an approved command's name
}

// Evidence is one recorded item. Content lives in the run repository's
// blob store under Blob, so later changes to the project can't alter it.
type Evidence struct {
	Kind      EvidenceKind `json:"kind"`
	Path      string       `json:"path,omitempty"`
	Paths     []string     `json:"paths,omitempty"`
	Baseline  string       `json:"baseline,omitempty"` // diff: the commit compared against
	Files     int          `json:"files,omitempty"`    // diff: number of files changed
	Absent    bool         `json:"absent,omitempty"`   // file: it didn't exist
	Command   string       `json:"command,omitempty"`  // command: its name
	Reviewer  string       `json:"reviewer,omitempty"` // review request or response: the reviewer
	Argv      []string     `json:"argv,omitempty"`     // command: what ran
	ExitCode  int          `json:"exitCode,omitempty"` // command: exit status
	Duration  int64        `json:"durationMs,omitempty"`
	TimedOut  bool         `json:"timedOut,omitempty"`
	Sandboxed bool         `json:"sandboxed,omitempty"` // command: ran under the sandbox
	Size      int64        `json:"size"`                // full content size in bytes
	SHA256    string       `json:"sha256,omitempty"`    // of the full content
	Blob      string       `json:"blob,omitempty"`      // key of the stored (possibly truncated) content
	Truncated bool         `json:"truncated,omitempty"`
	Error     string       `json:"error,omitempty"` // why it couldn't be collected
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// store records content in blobs, truncating at MaxEvidenceBytes, and fills
// in the item's size, hashes, and blob key.
func (e *Evidence) store(content []byte, blobs map[string][]byte) {
	e.Size = int64(len(content))
	e.SHA256 = sha(content)
	if len(content) > MaxEvidenceBytes {
		content = content[:MaxEvidenceBytes]
		e.Truncated = true
	}
	e.Blob = sha(content)
	blobs[e.Blob] = content
}

// collector gathers evidence for one attempt. Diffs are computed once per
// path set and shared by every rule that asks for them.
type collector struct {
	projectDir string
	baseline   string
	loadConfig func(projectDir string) (Config, error)
	keys       KeySource // API keys for reviewers that need them
	blobs      map[string][]byte
	diffs      map[string]Evidence
	commands   map[string]Evidence
	scratch    string // this attempt's scratch root, created on first use

	configOnce   sync.Once
	loadedConfig Config
	configErr    error
}

func newCollector(projectDir, baseline string, loadConfig func(string) (Config, error)) *collector {
	return &collector{
		projectDir: projectDir, baseline: baseline, loadConfig: loadConfig,
		blobs: map[string][]byte{}, diffs: map[string]Evidence{}, commands: map[string]Evidence{},
	}
}

// config loads the project's config the first time a command needs it, so
// a broken config only affects rules that run commands.
func (c *collector) config() (Config, error) {
	c.configOnce.Do(func() {
		if c.loadConfig == nil {
			c.loadedConfig = DefaultConfig()
			return
		}
		c.loadedConfig, c.configErr = c.loadConfig(c.projectDir)
	})
	return c.loadedConfig, c.configErr
}

// scratchDir returns an empty directory for one command or reviewer
// (kind) in this attempt.
func (c *collector) scratchDir(kind, name string) (string, error) {
	if c.scratch == "" {
		base := filepath.Join(c.projectDir, ".harness", "tmp")
		if err := os.MkdirAll(base, 0700); err != nil {
			return "", err
		}
		dir, err := os.MkdirTemp(base, "attempt-")
		if err != nil {
			return "", err
		}
		c.scratch = dir
	}
	dir, err := os.MkdirTemp(c.scratch, kind+"-"+name+"-")
	return dir, err
}

// cleanup removes this attempt's scratch directories. Caches stay.
func (c *collector) cleanup() {
	if c.scratch != "" {
		os.RemoveAll(c.scratch)
	}
}

func (c *collector) collect(spec EvidenceSpec) Evidence {
	switch spec.Kind {
	case FileEvidence:
		return c.file(spec.Path)
	case DiffEvidence:
		key := strings.Join(spec.Paths, "\x00")
		if cached, ok := c.diffs[key]; ok {
			return cached
		}
		item := c.diff(spec.Paths)
		c.diffs[key] = item
		return item
	case CommandEvidence:
		return c.command(spec.Command)
	default:
		return Evidence{Kind: spec.Kind, Error: fmt.Sprintf("unsupported evidence %q", spec.Kind)}
	}
}

// file reads a project file through os.Root, so symlinks can't lead outside
// the project.
func (c *collector) file(path string) Evidence {
	item := Evidence{Kind: FileEvidence, Path: path}
	root, err := os.OpenRoot(c.projectDir)
	if err != nil {
		item.Error = fmt.Sprintf("open project directory: %v", err)
		return item
	}
	defer root.Close()
	file, err := root.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		item.Absent = true
		return item
	}
	if err != nil {
		item.Error = err.Error()
		return item
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		item.Error = err.Error()
		return item
	}
	if !info.Mode().IsRegular() {
		item.Error = fmt.Sprintf("%s is not a regular file", path)
		return item
	}
	content, err := io.ReadAll(file)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	item.store(content, c.blobs)
	return item
}

// diff records every change since the baseline, untracked files included,
// without touching the project's own git index: files are staged into a
// temporary index, which is compared with the baseline commit.
func (c *collector) diff(paths []string) Evidence {
	item := Evidence{Kind: DiffEvidence, Paths: paths, Baseline: c.baseline}
	if c.baseline == "" {
		item.Error = "the project isn't a git repository, so there is no baseline to compare with"
		return item
	}
	stateDir := filepath.Join(c.projectDir, ".harness", "tmp")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		item.Error = err.Error()
		return item
	}
	index, err := os.CreateTemp(stateDir, "diff-index-")
	if err != nil {
		item.Error = err.Error()
		return item
	}
	index.Close()
	os.Remove(index.Name()) // git creates it; an empty file isn't a valid index
	defer os.Remove(index.Name())

	selected := []string{"."}
	if len(paths) > 0 {
		selected = paths
	}
	env := []string{"GIT_INDEX_FILE=" + index.Name()}
	// git add rejects paths that don't exist. A path that is gone or not
	// created yet still appears in the diff through the baseline side.
	var present []string
	for _, path := range selected {
		if _, err := os.Lstat(filepath.Join(c.projectDir, path)); err == nil {
			present = append(present, path)
		}
	}
	if len(present) > 0 {
		if _, err := git(c.projectDir, env, append([]string{"add", "--all", "--"}, present...)...); err != nil {
			item.Error = err.Error()
			return item
		}
	}
	// harness's own state never belongs in the diff, even if the project
	// doesn't ignore it.
	if _, err := git(c.projectDir, env, "rm", "-r", "-q", "--cached", "--ignore-unmatch", "--", ".harness"); err != nil {
		item.Error = err.Error()
		return item
	}
	pathspec := append(append([]string{"--"}, selected...), ":(exclude).harness")
	diffArgs := []string{"-c", "core.quotepath=false", "diff", "--cached", "--no-color", "--no-ext-diff", "--no-renames", "--binary"}
	content, err := git(c.projectDir, env, append(append(diffArgs, c.baseline), pathspec...)...)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	names, err := git(c.projectDir, env, append([]string{"diff", "--cached", "--no-renames", "--name-only", "-z", c.baseline}, pathspec...)...)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	item.Files = bytes.Count(names, []byte{0})
	item.store(content, c.blobs)
	return item
}

// Baseline returns the commit a project is at, EmptyTree for a repository
// with no commits, or "" when dir isn't in a git repository or git isn't
// installed.
func Baseline(dir string) string {
	if _, err := git(dir, nil, "rev-parse", "--is-inside-work-tree"); err != nil {
		return ""
	}
	head, err := git(dir, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return EmptyTree
	}
	return strings.TrimSpace(string(head))
}

// git runs a git command in dir with a timeout and returns its stdout.
func git(dir string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], message)
	}
	return stdout.Bytes(), nil
}
