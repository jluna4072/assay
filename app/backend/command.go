package backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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
	Sandbox  bool // run approved commands under the OS sandbox (the default)
	Commands map[string]CommandSpec
}

// DefaultConfig is the configuration of a project with no config file.
func DefaultConfig() Config { return Config{Sandbox: true, Commands: map[string]CommandSpec{}} }

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
	if config.Sandbox {
		if err := SandboxAvailable(); err != nil {
			item.Error = fmt.Sprintf("can't run commands sandboxed: %v. Set \"sandbox: off\" in .harness/config.yaml to run them without the sandbox", err)
			return item
		}
	}

	dir := c.projectDir
	if spec.Dir != "" && spec.Dir != "." {
		root, err := os.OpenRoot(c.projectDir)
		if err != nil {
			item.Error = err.Error()
			return item
		}
		info, err := root.Stat(spec.Dir)
		root.Close()
		if err != nil || !info.IsDir() {
			item.Error = fmt.Sprintf("dir %q isn't a directory inside the project", spec.Dir)
			return item
		}
		dir = filepath.Join(c.projectDir, spec.Dir)
	}
	program, err := exec.LookPath(spec.Run[0])
	if err != nil {
		item.Error = fmt.Sprintf("can't find %q: %v", spec.Run[0], err)
		return item
	}

	state := filepath.Join(c.projectDir, ".harness")
	scratch, err := c.scratchDir(name)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	cache := filepath.Join(state, "cache", name)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=" + firstNonEmpty(os.Getenv("LANG"), "en_US.UTF-8"),
		"TMPDIR=" + scratch,
		"XDG_CACHE_HOME=" + filepath.Join(cache, "xdg"),
	}
	for _, variable := range spec.Env {
		if value, ok := os.LookupEnv(variable); ok {
			env = append(env, variable+"="+value)
		}
	}
	for _, variable := range append([]string{"xdg"}, spec.Caches...) {
		path := filepath.Join(cache, variable)
		if err := os.MkdirAll(path, 0700); err != nil {
			item.Error = err.Error()
			return item
		}
		if variable != "xdg" {
			env = append(env, variable+"="+path)
		}
	}

	argv := append([]string{program}, spec.Run[1:]...)
	if config.Sandbox {
		// The sandbox matches resolved paths (/var is /private/var on macOS),
		// so every path in the profile must be resolved too.
		params := []string{}
		for _, param := range [][2]string{{"PROJECT", c.projectDir}, {"STATE", state}, {"SCRATCH", scratch}, {"CACHE", cache}, {"HOME", os.Getenv("HOME")}} {
			params = append(params, "-D", param[0]+"="+resolvePath(param[1]))
		}
		argv = append(append([]string{sandboxExec, "-p", sandboxProfile(spec)}, params...), argv...)
		item.Sandboxed = true
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
	out := &output{hash: sha256.New()}
	cmd.Stdout, cmd.Stderr = out, out
	prepareProcessGroup(cmd)
	started := time.Now()
	err = cmd.Run()
	item.Duration = time.Since(started).Milliseconds()
	killProcessGroup(cmd) // nothing a command started outlives it

	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		item.TimedOut = true
		item.ExitCode = -1
	case err == nil:
	case errors.As(err, &exitErr):
		item.ExitCode = exitErr.ExitCode()
	default:
		item.Error = fmt.Sprintf("run %s: %v", name, err)
		return item
	}
	item.Size = out.size
	item.SHA256 = hex.EncodeToString(out.hash.Sum(nil))
	kept := out.kept.Bytes()
	item.Truncated = int64(len(kept)) < out.size
	item.Blob = sha(kept)
	c.blobs[item.Blob] = kept
	return item
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
