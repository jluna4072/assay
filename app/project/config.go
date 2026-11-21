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

# Reviewers judge "check: review" rules: a model reads the rule's criterion
# and evidence and answers pass, fail, or insufficient_evidence. Contracts
# name a reviewer (reviewer: careful) or use the default, so each person
# chooses the model. Test one with "harness reviewer test <name>".
reviewers: {}
#  jev:                       # TypeSafe's Jev, with a TypeSafe console key
#    protocol: typesafe       # https://api.typesafe.ai/v1, model jev-latest
#    threshold: 0.9           # probability needed to pass (default)
#    # The key comes from TYPESAFE_API_KEY, or the Keychain: harness asks
#    # for it once, the first time it's needed.
#
#  jev-gateway:               # Jev through Vercel's AI Gateway, with a Vercel key
#    protocol: decisions      # any OpenAI-compatible Decisions API
#    endpoint: https://ai-gateway.vercel.sh/v1   # (default)
#    model: typesafe-ai/jev   # (default); key from AI_GATEWAY_API_KEY or the Keychain
#
#  claude:                    # a Claude model; it explains its verdicts
#    protocol: anthropic      # key from ANTHROPIC_API_KEY or the Keychain
#    model: claude-haiku-5-5  # default claude-opus-5-5; Haiku costs much less
#    effort: low              # optional: low, medium, high, xhigh, or max
#
#  local:                     # a model on this machine: free, no key
#    protocol: openai         # any OpenAI-compatible chat API
#    endpoint: http://localhost:11434/v1   # Ollama; LM Studio is :1234/v1
#    model: qwen3:8b          # the model's name on that server
#    temperature: 0           # optional
#
#  careful:                   # a program that reads a review request as JSON
#    protocol: command        # on stdin and writes its verdict on stdout

#    run: [my-reviewer]       # program and arguments; no shell
#    timeout: 2m              # at most 1h
#    network: on              # off, localhost (a local model), or on
#    env: [MY_API_KEY]        # variables passed through from your environment
# default_reviewer: careful   # needed only with more than one reviewer
`

type configFile struct {
	Version         *int                    `yaml:"version"`
	Sandbox         *string                 `yaml:"sandbox"`
	Commands        map[string]commandFile  `yaml:"commands"`
	Reviewers       map[string]reviewerFile `yaml:"reviewers"`
	DefaultReviewer string                  `yaml:"default_reviewer"`
}

type reviewerFile struct {
	Protocol string `yaml:"protocol"`
	Timeout  string `yaml:"timeout"`
	// command
	Run     []string `yaml:"run"`
	Network string   `yaml:"network"`
	Env     []string `yaml:"env"`
	Caches  []string `yaml:"caches"`
	// HTTP APIs: decisions, typesafe, openai, anthropic
	Endpoint    string   `yaml:"endpoint"`
	Model       string   `yaml:"model"`
	KeyEnv      string   `yaml:"key_env"`
	Threshold   *float64 `yaml:"threshold"`   // decisions, typesafe
	Temperature *float64 `yaml:"temperature"` // openai
	Effort      string   `yaml:"effort"`      // anthropic
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
		prefix := fmt.Sprintf("command %q: ", name)
		spec := checkProgram(name, prefix, command.Run, command.Timeout, command.Network, command.Env, command.Caches, add)
		spec.Dir = command.Dir
		if command.Dir != "" && command.Dir != "." && (!filepath.IsLocal(command.Dir) || strings.Contains(command.Dir, "\\")) {
			add("%sdir must be a relative path inside the project", prefix)
		}
		switch command.Writes {
		case "", "scratch":
		case "project":
			spec.WritesProject = true
		default:
			add("%swrites must be scratch or project", prefix)
		}
		config.Commands[name] = spec
	}
	for name, reviewer := range file.Reviewers {
		prefix := fmt.Sprintf("reviewer %q: ", name)
		spec := backend.ReviewerSpec{Name: name, Protocol: backend.ReviewProtocol(reviewer.Protocol)}
		unused := func(protocol string, keys map[string]bool) {
			for key, set := range keys {
				if set {
					add("%s%s isn't used by the %s protocol", prefix, key, protocol)
				}
			}
		}
		switch spec.Protocol {
		case backend.CommandProtocol:
			spec.Command = checkProgram(name, prefix, reviewer.Run, reviewer.Timeout, reviewer.Network, reviewer.Env, reviewer.Caches, add)
			unused("command", map[string]bool{"endpoint": reviewer.Endpoint != "", "model": reviewer.Model != "", "key_env": reviewer.KeyEnv != "", "threshold": reviewer.Threshold != nil})
		case backend.DecisionsProtocol, backend.TypeSafeProtocol, backend.OpenAIProtocol, backend.AnthropicProtocol:
			if !backend.CommandName(name) {
				add("%sname must be letters, digits, '.', '_', or '-'", prefix)
			}
			unused(string(spec.Protocol), map[string]bool{"run": len(reviewer.Run) > 0, "network": reviewer.Network != "", "env": len(reviewer.Env) > 0, "caches": len(reviewer.Caches) > 0})
			decisions := backend.APISpec{Endpoint: reviewer.Endpoint, Model: reviewer.Model, KeyEnv: reviewer.KeyEnv, Temperature: reviewer.Temperature, Effort: reviewer.Effort}
			decisionModel := backend.DecisionModel(spec.Protocol)
			unused(string(spec.Protocol), map[string]bool{
				"threshold":   reviewer.Threshold != nil && !decisionModel,
				"temperature": reviewer.Temperature != nil && spec.Protocol != backend.OpenAIProtocol,
				"effort":      reviewer.Effort != "" && spec.Protocol != backend.AnthropicProtocol,
			})
			if spec.Protocol == backend.OpenAIProtocol && reviewer.Model == "" {
				add("%smodel is required for the openai protocol: the model's name at that service, or on your local server", prefix)
			}
			if t := reviewer.Temperature; t != nil && (*t < 0 || *t > 2) {
				add("%stemperature must be between 0 and 2", prefix)
			}
			switch reviewer.Effort {
			case "", "low", "medium", "high", "xhigh", "max":
			default:
				add("%seffort must be low, medium, high, xhigh, or max", prefix)
			}
			if reviewer.Endpoint != "" {
				if err := backend.CheckEndpoint(reviewer.Endpoint); err != nil {
					add("%s%v", prefix, err)
				}
			}
			if reviewer.KeyEnv != "" && !variableName.MatchString(reviewer.KeyEnv) {
				add("%skey_env %q isn't an environment variable name", prefix, reviewer.KeyEnv)
			}
			if t := reviewer.Threshold; t != nil && decisionModel {
				if *t <= 0 || *t > 1 {
					add("%sthreshold must be more than 0 and at most 1, like 0.9", prefix)
				}
				decisions.Threshold = *t
			}
			if reviewer.Timeout != "" {
				timeout, err := time.ParseDuration(reviewer.Timeout)
				if err != nil || timeout < time.Second || timeout > MaxCommandTimeout {
					add("%stimeout must be a duration between 1s and %s, like 2m", prefix, MaxCommandTimeout)
				}
				decisions.Timeout = timeout
			}
			spec.API = decisions
		case "":
			add("%sprotocol is required; this harness supports: command, typesafe, decisions, openai, anthropic", prefix)
		default:
			add("%sprotocol %q isn't supported by this harness; it supports: command, typesafe, decisions, openai, anthropic", prefix, reviewer.Protocol)
		}
		config.Reviewers[name] = spec
	}
	if file.DefaultReviewer != "" {
		if _, ok := file.Reviewers[file.DefaultReviewer]; !ok {
			add("default_reviewer %q isn't one of the reviewers configured here", file.DefaultReviewer)
		}
		config.DefaultReviewer = file.DefaultReviewer
	}
	// Commands are a map, so sort problems for stable output.
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].Message < problems[j].Message })
	return config, problems
}

// checkProgram checks the settings commands and command reviewers share
// and returns them as a CommandSpec.
func checkProgram(name, prefix string, run []string, timeout, network string, env, caches []string, add func(string, ...any)) backend.CommandSpec {
	spec := backend.CommandSpec{Name: name, Run: run, Env: env, Caches: caches}
	if !backend.CommandName(name) {
		add("%sname must be letters, digits, '.', '_', or '-'", prefix)
	}
	if len(run) == 0 || strings.TrimSpace(run[0]) == "" {
		add("%srun needs a program and its arguments, like [go, test, ./...]", prefix)
	} else if strings.HasPrefix(run[0], "-") {
		add("%srun's program can't start with '-'", prefix)
	}
	if timeout != "" {
		parsed, err := time.ParseDuration(timeout)
		if err != nil || parsed < time.Second || parsed > MaxCommandTimeout {
			add("%stimeout must be a duration between 1s and %s, like 5m", prefix, MaxCommandTimeout)
		}
		spec.Timeout = parsed
	}
	switch backend.NetworkAccess(network) {
	case "", backend.NetworkOff:
		spec.Network = backend.NetworkOff
	case backend.NetworkLocalhost, backend.NetworkOn:
		spec.Network = backend.NetworkAccess(network)
	default:
		add("%snetwork must be off, localhost, or on", prefix)
	}
	for _, variable := range append(append([]string{}, env...), caches...) {
		if !variableName.MatchString(variable) {
			add("%s%q isn't an environment variable name", prefix, variable)
		} else if reservedVariables[variable] {
			add("%s%s is set by harness and can't be configured", prefix, variable)
		}
	}
	return spec
}
