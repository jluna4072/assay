package project

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"harness/backend"
	"harness/contractfile"
)

// ConfigFile holds a project's local settings, inside StateDir. Only the
// user edits it; it isn't committed, so agents and contracts can't change
// which commands are approved.
const ConfigFile = "config.yaml"

// MaxCommandTimeout bounds an approved command's timeout.
const MaxCommandTimeout = time.Hour

func (p Project) ConfigPath() string { return filepath.Join(p.Root, StateDir, ConfigFile) }

// ConfigTemplate is written by harness init.
const ConfigTemplate = `# harness local settings for this project. Only you edit this file; it
# lives in .harness/, which isn't committed.
version: 1

# Approved commands run under the macOS sandbox: no network unless allowed
# below, writes only to a temporary directory and declared caches, and no
# reading of credentials such as ~/.ssh. Set "sandbox: off" only on a
# machine without the sandbox; attempts run that way are marked unsandboxed.
sandbox: on

# Commands contracts may run, by name. A rule uses one with
#   check: command_succeeds
#   command: go-test
# and evidence can record one with "- command: go-test".
commands: {}
#  go-test:
#    run: [go, test, ./...]   # program and arguments; no shell
#    dir: .                   # where to run it, relative to the project
#    timeout: 5m              # at most 1h
#    network: off             # off, localhost, or on
#    writes: scratch          # scratch (a temporary directory) or project
#    env: [GOFLAGS]           # variables passed through from your environment
#    caches: [GOCACHE]        # variables pointed at .harness/cache/go-test/<NAME>
`

type configFile struct {
	Version  *int                   `yaml:"version"`
	Sandbox  *string                `yaml:"sandbox"`
	Commands map[string]commandFile `yaml:"commands"`
}

type commandFile struct {
	Run     []string `yaml:"run"`
	Dir     string   `yaml:"dir"`
	Timeout string   `yaml:"timeout"`
	Network string   `yaml:"network"`
	Writes  string   `yaml:"writes"`
	Env     []string `yaml:"env"`
	Caches  []string `yaml:"caches"`
}

var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedVariables are set by harness for every command.
var reservedVariables = map[string]bool{"PATH": true, "HOME": true, "LANG": true, "TMPDIR": true, "XDG_CACHE_HOME": true}

// LoadConfig reads the config of the project rooted at root. A missing
// file is the default config. Problems are reported together, with line
// numbers, as one error.
func LoadConfig(root string) (backend.Config, error) {
	path := filepath.Join(root, StateDir, ConfigFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return backend.DefaultConfig(), nil
	}
	if err != nil {
		return backend.Config{}, err
	}
	config, problems := ParseConfig(data)
	if len(problems) > 0 {
		lines := make([]string, len(problems))
		for i, p := range problems {
			lines[i] = filepath.Join(StateDir, ConfigFile) + ":" + p.String()
		}
		return backend.Config{}, fmt.Errorf("invalid config:\n  %s", strings.Join(lines, "\n  "))
	}
	return config, nil
}

// ParseConfig decodes and checks config YAML.
func ParseConfig(data []byte) (backend.Config, []contractfile.Problem) {
	config := backend.DefaultConfig()
	var problems []contractfile.Problem
	add := func(format string, args ...any) {
		problems = append(problems, contractfile.Problem{Message: fmt.Sprintf(format, args...)})
	}

	var file configFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		var typeErr *yaml.TypeError
		if !errors.As(err, &typeErr) {
			return config, []contractfile.Problem{contractfile.YAMLProblem(err)}
		}
		for _, message := range typeErr.Errors {
			problems = append(problems, contractfile.YAMLProblem(errors.New(message)))
		}
		return config, problems
	}
	if file.Version != nil && *file.Version != 1 {
		add("unsupported config version %d; this harness reads version 1", *file.Version)
	}
	if file.Sandbox != nil {
		switch strings.ToLower(*file.Sandbox) {
		case "on", "true":
		case "off", "false":
			config.Sandbox = false
		default:
			add("sandbox must be on or off, not %q", *file.Sandbox)
		}
	}
	for name, command := range file.Commands {
		spec := backend.CommandSpec{Name: name, Run: command.Run, Dir: command.Dir, Env: command.Env, Caches: command.Caches}
		prefix := fmt.Sprintf("command %q: ", name)
		if !backend.CommandName(name) {
			add("%sname must be letters, digits, '.', '_', or '-'", prefix)
		}
		if len(command.Run) == 0 || strings.TrimSpace(command.Run[0]) == "" {
			add("%srun needs a program and its arguments, like [go, test, ./...]", prefix)
		} else if strings.HasPrefix(command.Run[0], "-") {
			add("%srun's program can't start with '-'", prefix)
		}
		if command.Dir != "" && command.Dir != "." && (!filepath.IsLocal(command.Dir) || strings.Contains(command.Dir, "\\")) {
			add("%sdir must be a relative path inside the project", prefix)
		}
		if command.Timeout != "" {
			timeout, err := time.ParseDuration(command.Timeout)
			if err != nil || timeout <= 0 || timeout > MaxCommandTimeout {
				add("%stimeout must be a duration between 1s and %s, like 5m", prefix, MaxCommandTimeout)
			}
			spec.Timeout = timeout
		}
		switch backend.NetworkAccess(command.Network) {
		case "", backend.NetworkOff:
			spec.Network = backend.NetworkOff
		case backend.NetworkLocalhost, backend.NetworkOn:
			spec.Network = backend.NetworkAccess(command.Network)
		default:
			add("%snetwork must be off, localhost, or on", prefix)
		}
		switch command.Writes {
		case "", "scratch":
		case "project":
			spec.WritesProject = true
		default:
			add("%swrites must be scratch or project", prefix)
		}
		for _, variable := range append(append([]string{}, command.Env...), command.Caches...) {
			if !variableName.MatchString(variable) {
				add("%s%q isn't an environment variable name", prefix, variable)
			} else if reservedVariables[variable] {
				add("%s%s is set by harness and can't be configured", prefix, variable)
			}
		}
		config.Commands[name] = spec
	}
	// Commands are a map, so sort problems for stable output.
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].Message < problems[j].Message })
	return config, problems
}
