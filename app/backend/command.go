package backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// NetworkAccess is what an approved command may reach.
type NetworkAccess string

const (
	NetworkOff       NetworkAccess = "off"
	NetworkLocalhost NetworkAccess = "localhost"
	NetworkOn        NetworkAccess = "on"
)

// DefaultCommandTimeout applies when an approved command sets none.
const DefaultCommandTimeout = 5 * time.Minute

// CommandSpec is an approved command from the project's local config.
// Contracts refer to it by name; they can't change what it runs.
type CommandSpec struct {
	Name          string
	Run           []string      // argv; never passed through a shell
	Dir           string        // project-relative working directory; "" is the project root
	Timeout       time.Duration // DefaultCommandTimeout when zero
	Network       NetworkAccess // NetworkOff when empty
	WritesProject bool          // may write anywhere in the project except .harness/
	Env           []string      // variable names passed through from harness's environment
	Caches        []string      // variable names pointed at persistent per-command cache directories
}

// Config is a project's local settings.
type Config struct {
	Sandbox   bool // run approved commands and reviewers under the OS sandbox (the default)
	Commands  map[string]CommandSpec
	Reviewers map[string]ReviewerSpec
	// DefaultReviewer reviews rules that don't name a reviewer. When it's
	// empty and exactly one reviewer is configured, that one is the default.
	DefaultReviewer string
}

// DefaultConfig is the configuration of a project with no config file.
func DefaultConfig() Config {
	return Config{Sandbox: true, Commands: map[string]CommandSpec{}, Reviewers: map[string]ReviewerSpec{}}
}

// Reviewer returns the reviewer a rule names, or the default reviewer when
// name is empty.
func (c Config) Reviewer(name string) (ReviewerSpec, error) {
	if name == "" {
		name = c.DefaultReviewer
		if name == "" {
			switch len(c.Reviewers) {
			case 0:
				return ReviewerSpec{}, fmt.Errorf("no reviewers are configured in .harness/config.yaml")
			case 1:
				for _, only := range c.Reviewers {
					return only, nil
				}
			default:
				return ReviewerSpec{}, fmt.Errorf("the rule doesn't name a reviewer and .harness/config.yaml sets no default_reviewer")
			}
		}
	}
	spec, ok := c.Reviewers[name]
	if !ok {
		return ReviewerSpec{}, fmt.Errorf("reviewer %q isn't configured in .harness/config.yaml", name)
	}
	return spec, nil
}

// SandboxAvailable reports whether approved commands can run sandboxed on
// this machine, and why not. It is a variable so tests can simulate a
// machine without a sandbox.
var SandboxAvailable = func() error {
	sandboxOnce.Do(func() { sandboxErr = probeSandbox() })
	return sandboxErr
}

var (
	sandboxOnce sync.Once
	sandboxErr  error
)

const sandboxExec = "/usr/bin/sandbox-exec"

func probeSandbox() error {
	if _, err := os.Stat(sandboxExec); err != nil {
		return fmt.Errorf("%s isn't available on this system", sandboxExec)
	}
	out, err := exec.Command(sandboxExec, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s doesn't work here: %s", sandboxExec, strings.TrimSpace(string(out)+" "+err.Error()))
	}
	return nil
}

// sandboxProfile builds the Seatbelt profile for a command. Paths are
// passed as parameters, never spliced into the profile text. Later rules
// override earlier ones.
func sandboxProfile(spec CommandSpec) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	switch spec.Network {
	case NetworkOn:
	case NetworkLocalhost:
		b.WriteString("(deny network*)\n(allow network* (remote ip \"localhost:*\"))\n(allow network-bind network-inbound (local ip \"localhost:*\"))\n")
	default:
		b.WriteString("(deny network*)\n")
	}
	b.WriteString("(deny file-write*)\n")
	if spec.WritesProject {
		b.WriteString("(allow file-write* (subpath (param \"PROJECT\")))\n(deny file-write* (subpath (param \"STATE\")))\n")
	}
	b.WriteString(`(allow file-write*
  (subpath (param "SCRATCH"))
  (subpath (param "CACHE"))
  (literal "/dev/null") (literal "/dev/zero") (literal "/dev/dtracehelper")
  (regex #"^/dev/tty") (regex #"^/dev/fd/"))
(deny file-read*
  (subpath (string-append (param "HOME") "/.ssh"))
  (subpath (string-append (param "HOME") "/.aws"))
  (subpath (string-append (param "HOME") "/.gnupg"))
  (subpath (string-append (param "HOME") "/.docker"))
  (subpath (string-append (param "HOME") "/.config/gh"))
  (subpath (string-append (param "HOME") "/Library/Keychains"))
  (literal (string-append (param "HOME") "/.netrc"))
  (literal (string-append (param "STATE") "/harness.db"))
  (literal (string-append (param "STATE") "/harness.db-journal"))
  (literal (string-append (param "STATE") "/harness.db-wal"))
  (literal (string-append (param "STATE") "/harness.db-shm")))
`)
	return b.String()
}

// output records a command's combined stdout and stderr: the first
// MaxEvidenceBytes, plus the full size and hash.
type output struct {
	mu   sync.Mutex
	kept bytes.Buffer
	size int64
	hash hash.Hash
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hash.Write(p)
	o.size += int64(len(p))
	if room := MaxEvidenceBytes - o.kept.Len(); room > 0 {
		o.kept.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// command runs an approved command once per attempt and records it as
// evidence. Later requests for the same command reuse the result.
func (c *collector) command(name string) Evidence {
	if cached, ok := c.commands[name]; ok {
		return cached
	}
	item := c.runCommand(name)
	c.commands[name] = item
	return item
}

func (c *collector) runCommand(name string) Evidence {
	item := Evidence{Kind: CommandEvidence, Command: name}
	config, err := c.config()
	if err != nil {
		item.Error = err.Error()
		return item
	}
	spec, ok := config.Commands[name]
	if !ok {
		item.Error = fmt.Sprintf("command %q isn't approved in .harness/config.yaml", name)
		return item
	}
	item.Argv = spec.Run
	scratch, err := c.scratchDir("command", name)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	out := &output{hash: sha256.New()}
	result, err := runProcess(process{
		spec: spec, sandbox: config.Sandbox, project: c.projectDir, scratch: scratch,
		cache:  filepath.Join(c.projectDir, ".harness", "cache", name),
		stdout: out, stderr: out,
	})
	if err != nil {
		item.Error = err.Error()
		return item
	}
	item.Duration, item.TimedOut, item.ExitCode, item.Sandboxed = result.duration, result.timedOut, result.exitCode, result.sandboxed
	item.Size = out.size
	item.SHA256 = hex.EncodeToString(out.hash.Sum(nil))
	kept := out.kept.Bytes()
	item.Truncated = int64(len(kept)) < out.size
	item.Blob = sha(kept)
	c.blobs[item.Blob] = kept
	return item
}

// process is one run of an approved program: a command or a reviewer.
type process struct {
	spec    CommandSpec
	sandbox bool   // run under the OS sandbox
	project string // the project root
	scratch string // an empty directory for this run; TMPDIR
	cache   string // persistent cache root for spec.Caches
	stdin   []byte // nil for no input
	stdout  io.Writer
	stderr  io.Writer
}

type processResult struct {
	exitCode  int // -1 when timed out
	timedOut  bool
	duration  int64 // milliseconds
	sandboxed bool
}

// runProcess runs p with a cleared environment, in its own process group,
// under the sandbox when p.sandbox is set. The error is non-nil only when
// the program couldn't be started; exit statuses and timeouts are results.
func runProcess(p process) (processResult, error) {
	var result processResult
	spec := p.spec
	if p.sandbox {
		if err := SandboxAvailable(); err != nil {
			return result, fmt.Errorf("can't run %s sandboxed: %v. Set \"sandbox: off\" in .harness/config.yaml to run it without the sandbox", spec.Name, err)
		}
	}
	dir := p.project
	if spec.Dir != "" && spec.Dir != "." {
		root, err := os.OpenRoot(p.project)
		if err != nil {
			return result, err
		}
		info, err := root.Stat(spec.Dir)
		root.Close()
		if err != nil || !info.IsDir() {
			return result, fmt.Errorf("dir %q isn't a directory inside the project", spec.Dir)
		}
		dir = filepath.Join(p.project, spec.Dir)
	}
	program, err := exec.LookPath(spec.Run[0])
	if err != nil {
		return result, fmt.Errorf("can't find %q: %v", spec.Run[0], err)
	}

	state := filepath.Join(p.project, ".harness")
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=" + firstNonEmpty(os.Getenv("LANG"), "en_US.UTF-8"),
		"TMPDIR=" + p.scratch,
		"XDG_CACHE_HOME=" + filepath.Join(p.cache, "xdg"),
	}
	for _, variable := range spec.Env {
		if value, ok := os.LookupEnv(variable); ok {
			env = append(env, variable+"="+value)
		}
	}
	for _, variable := range append([]string{"xdg"}, spec.Caches...) {
		path := filepath.Join(p.cache, variable)
		if err := os.MkdirAll(path, 0700); err != nil {
			return result, err
		}
		if variable != "xdg" {
			env = append(env, variable+"="+path)
		}
	}

	argv := append([]string{program}, spec.Run[1:]...)
	if p.sandbox {
		// The sandbox matches resolved paths (/var is /private/var on macOS),
		// so every path in the profile must be resolved too.
		params := []string{}
		for _, param := range [][2]string{{"PROJECT", p.project}, {"STATE", state}, {"SCRATCH", p.scratch}, {"CACHE", p.cache}, {"HOME", os.Getenv("HOME")}} {
			params = append(params, "-D", param[0]+"="+resolvePath(param[1]))
		}
		argv = append(append([]string{sandboxExec, "-p", sandboxProfile(spec)}, params...), argv...)
		result.sandboxed = true
	}

	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	if p.stdin != nil {
		cmd.Stdin = bytes.NewReader(p.stdin)
	}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	prepareProcessGroup(cmd)
	started := time.Now()
	err = cmd.Run()
	result.duration = time.Since(started).Milliseconds()
	killProcessGroup(cmd) // nothing a command started outlives it

	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.timedOut = true
		result.exitCode = -1
	case err == nil:
	case errors.As(err, &exitErr):
		result.exitCode = exitErr.ExitCode()
	default:
		return result, fmt.Errorf("run %s: %v", spec.Name, err)
	}
	return result, nil
}

// commandVerdict turns a command_succeeds rule's command result into the
// rule's status and reason.
func commandVerdict(rule Rule, item Evidence) (ResultStatus, string) {
	switch {
	case item.Error != "":
		return OperationalError, item.Error
	case item.TimedOut:
		return Failed, fmt.Sprintf("%s timed out after %s", rule.Command, (time.Duration(item.Duration) * time.Millisecond).Round(time.Second))
	case item.ExitCode != 0:
		return Failed, fmt.Sprintf("%s exited with status %d", rule.Command, item.ExitCode)
	}
	return Passed, ""
}

// resolvePath returns path with symlinks resolved, or path itself if it
// can't be resolved (for example, because it doesn't exist).
func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
