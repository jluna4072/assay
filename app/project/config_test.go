package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/backend"
)

func TestConfigTemplateParses(t *testing.T) {
	config, problems := ParseConfig([]byte(ConfigTemplate))
	if len(problems) != 0 || !config.Sandbox || len(config.Commands) != 0 {
		t.Fatalf("template: %+v %v", config, problems)
	}
}

func TestParseConfig(t *testing.T) {
	config, problems := ParseConfig([]byte(`version: 1
sandbox: off
commands:
  go-test:
    run: [go, test, ./...]
    dir: app
    timeout: 90s
    network: localhost
    writes: project
    env: [GOFLAGS]
    caches: [GOCACHE]
  plain:
    run: [make]
`))
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	want := backend.CommandSpec{Name: "go-test", Run: []string{"go", "test", "./..."}, Dir: "app", Timeout: 90 * time.Second,
		Network: backend.NetworkLocalhost, WritesProject: true, Env: []string{"GOFLAGS"}, Caches: []string{"GOCACHE"}}
	got := config.Commands["go-test"]
	if config.Sandbox || got.Name != want.Name || strings.Join(got.Run, " ") != "go test ./..." || got.Dir != want.Dir ||
		got.Timeout != want.Timeout || got.Network != want.Network || !got.WritesProject || got.Env[0] != "GOFLAGS" || got.Caches[0] != "GOCACHE" {
		t.Fatalf("go-test: %+v", got)
	}
	if plain := config.Commands["plain"]; plain.Network != backend.NetworkOff || plain.WritesProject || plain.Timeout != 0 {
		t.Fatalf("defaults: %+v", plain)
	}
}

func TestParseConfigProblems(t *testing.T) {
	_, problems := ParseConfig([]byte(`version: 2
sandbox: maybe
commands:
  "bad name":
    run: [ls]
  empty: {}
  flags:
    run: [-rf]
  escape:
    run: [ls]
    dir: ../up
    timeout: 3h
    network: sometimes
    writes: everywhere
    env: [PATH, "1BAD"]
`))
	got := make([]string, len(problems))
	for i, p := range problems {
		got[i] = p.String()
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"unsupported config version 2",
		`sandbox must be on or off, not "maybe"`,
		`command "bad name": name must be letters`,
		`command "empty": run needs a program`,
		`command "flags": run's program can't start with '-'`,
		`command "escape": dir must be a relative path inside the project`,
		`command "escape": timeout must be a duration between 1s and 1h0m0s`,
		`command "escape": network must be off, localhost, or on`,
		`command "escape": writes must be scratch or project`,
		`command "escape": PATH is set by harness`,
		`command "escape": "1BAD" isn't an environment variable name`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems lack %q:\n%s", want, joined)
		}
	}

	_, problems = ParseConfig([]byte("commands:\n  x:\n    run: [ls]\n    shell: true\n"))
	if len(problems) != 1 || problems[0].String() != `4: unknown key "shell"` {
		t.Fatalf("unknown key: %v", problems)
	}
}

func TestLoadConfig(t *testing.T) {
	root := t.TempDir()
	if config, err := LoadConfig(root); err != nil || !config.Sandbox {
		t.Fatalf("missing file: %+v %v", config, err)
	}
	os.MkdirAll(filepath.Join(root, StateDir), 0700)
	os.WriteFile(filepath.Join(root, StateDir, ConfigFile), []byte("sandbox: sometimes\n"), 0600)
	if _, err := LoadConfig(root); err == nil || !strings.Contains(err.Error(), ".harness/config.yaml:") {
		t.Fatalf("invalid file: %v", err)
	}
}

func TestParseReviewers(t *testing.T) {
	config, problems := ParseConfig([]byte(`version: 1
reviewers:
  careful:
    protocol: command
    run: [my-reviewer, --strict]
    timeout: 3m
    network: on
    env: [MY_API_KEY]
  local:
    protocol: command
    run: [local-reviewer]
    network: localhost
default_reviewer: careful
`))
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	careful := config.Reviewers["careful"]
	if careful.Name != "careful" || careful.Protocol != backend.CommandProtocol || strings.Join(careful.Command.Run, " ") != "my-reviewer --strict" ||
		careful.Command.Timeout != 3*time.Minute || careful.Command.Network != backend.NetworkOn || careful.Command.Env[0] != "MY_API_KEY" {
		t.Fatalf("careful: %+v", careful)
	}
	if spec, err := config.Reviewer(""); err != nil || spec.Name != "careful" {
		t.Fatalf("default: %+v %v", spec, err)
	}

	_, problems = ParseConfig([]byte(`reviewers:
  noproto:
    run: [x]
  future:
    protocol: decisions
  norun:
    protocol: command
  writer:
    protocol: command
    run: [x]
    writes: project
default_reviewer: missing
`))
	got := make([]string, len(problems))
	for i, p := range problems {
		got[i] = p.String()
	}
	want := []string{
		"11: unknown key \"writes\"",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("decode problems:\n%s", strings.Join(got, "\n"))
	}
	_, problems = ParseConfig([]byte(`reviewers:
  noproto:
    run: [x]
  future:
    protocol: decisions
  norun:
    protocol: command
default_reviewer: missing
`))
	got = got[:0]
	for _, p := range problems {
		got = append(got, p.String())
	}
	want = []string{
		`default_reviewer "missing" isn't one of the reviewers configured here`,
		`reviewer "future": protocol "decisions" isn't supported by this harness; it supports: command`,
		`reviewer "noproto": protocol is required; this harness supports: command`,
		`reviewer "norun": run needs a program and its arguments, like [go, test, ./...]`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("problems:\n%s", strings.Join(got, "\n"))
	}
}
