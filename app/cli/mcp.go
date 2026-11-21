package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"assay/project"
)

var mcpCommand = command{
	name:    "mcp",
	args:    "",
	summary: "Serve this project's contracts and runs to an agent over MCP",
	details: `
Runs a Model Context Protocol server on stdin and stdout for the project, so
an agent such as Claude Code can list and validate contracts, check the
project, and read runs and evidence. Each tool runs the matching command
with --json and returns what it prints. There is no tool to publish:
publishing stays with you.

Add it to Claude Code from the project directory:
  claude mcp add assay -- assay mcp

Flags:
` + projectFlagHelp + `
`,
}

// The server runs commands through Run, which looks them up in commands,
// so its run function is set here rather than in the literal above (and
// on the copy in commands).
func init() {
	for i := range commands {
		if commands[i].name == mcpCommand.name {
			commands[i].run = runMCP
		}
	}
}

const mcpInstructions = `assay checks work against contracts and records the evidence for every verdict. Only assay's verdict counts: work is done when check (or recheck) reports every rule passed, not before.

Contracts are YAML files in the project's assay/ directory (see get_contract for the format of an existing one). A suite, in assay/suites/<id>/, groups contract files under a suite.yaml and is validated, published, and checked as one contract whose rules are named <contract>/<rule>. To add or change one, write the file, run validate_contract until it has no problems, then show the person diff_contract and ask them to publish it ("assay contract publish <file>", or p in the assay UI). You can't publish.

get_contract shows what each rule requires: its criterion, its check, the rules it depends on, and the evidence it records. Read it before working toward a contract.

check starts a run of a published contract and returns its first attempt; after changing the project, recheck adds an attempt to the same run. When a rule fails, read its reason in the result, and get_evidence shows exactly what was checked (file contents, diffs, command output, and what a reviewer was sent and answered).`

func runMCP(e Env, cmd *command, args []string) int {
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	projectDir := fs.String("project", "", "")
	rest, code, ok := parseFlags(e, cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(e.Stderr, "assay mcp: unexpected argument %q\n", rest[0])
		return ExitUsage
	}
	var p project.Project
	var err error
	if *projectDir != "" {
		p, err = project.Open(resolve(e, *projectDir))
	} else {
		p, err = project.Find(e.Dir)
	}
	if errors.Is(err, project.ErrNotFound) {
		fmt.Fprintf(e.Stderr, "assay mcp: %v\nRun \"assay init\" in the project, then start the server there.\n", err)
		return ExitUsage
	}
	if err != nil {
		fmt.Fprintf(e.Stderr, "assay mcp: %v\n", err)
		return ExitOperational
	}
	ctx, stop := interruptible(e)
	defer stop()
	if err := newMCPServer(p.Root).Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		fmt.Fprintf(e.Stderr, "assay mcp: %v\n", err)
		return ExitOperational
	}
	return ExitOK
}

// mcpTool runs one command for a tool call.
type mcpTool struct {
	root string
}

// run runs "assay <args> --json" in the project and turns what it printed
// into the tool's result. A negative verdict (exit 1) is a result, not an
// error; a usage or operational error is.
func (t mcpTool) run(ctx context.Context, args ...string) *mcp.CallToolResult {
	var stdout, stderr bytes.Buffer
	code := Run(args, Env{Stdout: &stdout, Stderr: &stderr, Dir: t.root, Context: ctx})
	result := &mcp.CallToolResult{IsError: code == ExitUsage || code == ExitOperational}
	if text := strings.TrimSpace(stdout.String()); text != "" {
		result.Content = append(result.Content, &mcp.TextContent{Text: text})
	}
	if code != ExitOK || stderr.Len() > 0 {
		note := fmt.Sprintf("assay exited %d (%s)", code, exitMeaning[code])
		if text := strings.TrimSpace(stderr.String()); text != "" {
			note += ": " + text
		}
		result.Content = append(result.Content, &mcp.TextContent{Text: note})
	}
	return result
}

var exitMeaning = map[int]string{
	ExitOK:          "success",
	ExitNegative:    "a negative verdict: a rule failed or was blocked, or the file has problems",
	ExitOperational: "something couldn't run",
	ExitUsage:       "a usage error",
}

// inProject checks that a path the agent gave is inside the project and
// returns it relative to the root.
func (t mcpTool) inProject(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is required")
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(t.root, full)
	}
	rel, err := filepath.Rel(t.root, filepath.Clean(full))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the project", path)
	}
	return rel, nil
}

func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}

func required(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

// Tool inputs. Descriptions become the tools' JSON schema.
type (
	noInput     struct{}
	contractRef struct {
		Contract string `json:"contract" jsonschema:"a published contract: its ID for the latest version, or id@version"`
	}
	contractFile struct {
		Path string `json:"path" jsonschema:"the contract file, relative to the project root, e.g. assay/docs.yaml"`
	}
	runRef struct {
		Run string `json:"run,omitempty" jsonschema:"a run ID or any unique prefix; the most recent run when empty"`
	}
	runsInput struct {
		Contract string `json:"contract,omitempty" jsonschema:"only runs of this contract ID"`
		Limit    int    `json:"limit,omitempty" jsonschema:"at most this many runs, newest first (default 20)"`
	}
	getRunInput struct {
		Run      string `json:"run,omitempty" jsonschema:"a run ID or any unique prefix; the most recent run when empty"`
		Attempt  int    `json:"attempt,omitempty" jsonschema:"only this attempt (1-based)"`
		Evidence bool   `json:"evidence,omitempty" jsonschema:"include each result's evidence list"`
	}
	evidenceInput struct {
		Run     string `json:"run,omitempty" jsonschema:"a run ID or any unique prefix; the most recent run when empty"`
		Rule    string `json:"rule" jsonschema:"the rule ID"`
		Attempt int    `json:"attempt,omitempty" jsonschema:"the attempt (1-based); the latest when empty"`
		Item    int    `json:"item,omitempty" jsonschema:"only this evidence item (1-based)"`
	}
)

func boolPtr(b bool) *bool { return &b }

var (
	readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true}
	// Checks record an attempt and may run approved commands and ask
	// reviewers, but change nothing in the project.
	checks = &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(true)}
)

func newMCPServer(root string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "assay", Version: version()}, &mcp.ServerOptions{Instructions: mcpInstructions})
	t := mcpTool{root: root}
	// Commands find the project from the directory they run in, the root.
	with := func(args ...string) []string { return append(args, "--json") }

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_contracts",
		Description: "List the contract files in assay/ (with whether each is valid and whether it is published as is, changed since its latest version, or unpublished) and the published contracts with their latest versions.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
		return t.run(ctx, with("contract", "list")...), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_contract",
		Description: "Get a published contract version: its rules, each with a criterion, a check, dependencies, and the evidence it records.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in contractRef) (*mcp.CallToolResult, any, error) {
		if err := required("contract", in.Contract); err != nil {
			return toolError(err), nil, nil
		}
		return t.run(ctx, append(with("contract", "show"), "--", in.Contract)...), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "validate_contract",
		Description: "Validate a contract file, or a suite folder (a suite.yaml and the contract files it groups), without publishing it. Reports every problem with its line: unknown keys, missing fields, unknown dependencies or cycles, commands not approved in .assay/config.yaml, and unknown reviewers.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in contractFile) (*mcp.CallToolResult, any, error) {
		path, err := t.inProject(in.Path)
		if err != nil {
			return toolError(err), nil, nil
		}
		return t.run(ctx, append(with("contract", "validate"), "--", path)...), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "diff_contract",
		Description: "Show what publishing a contract file would change compared with the latest published version. Show this to the person when asking them to publish.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in contractFile) (*mcp.CallToolResult, any, error) {
		path, err := t.inProject(in.Path)
		if err != nil {
			return toolError(err), nil, nil
		}
		return t.run(ctx, append(with("contract", "diff"), "--", path)...), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "check",
		Description: "Start a run of a published contract and check the project against it. Returns the run and its first attempt: each rule passed, failed (with the reason), blocked by a failed dependency, or errored. The work is done only when every rule passed.",
		Annotations: checks,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in contractRef) (*mcp.CallToolResult, any, error) {
		if err := required("contract", in.Contract); err != nil {
			return toolError(err), nil, nil
		}
		return t.run(ctx, append(with("check"), "--", in.Contract)...), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "recheck",
		Description: "Check the project again for an existing run, adding an attempt; earlier attempts are kept. Use it after fixing what a check reported.",
		Annotations: checks,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runRef) (*mcp.CallToolResult, any, error) {
		args := with("recheck")
		if in.Run != "" {
			args = append(args, "--", in.Run)
		}
		return t.run(ctx, args...), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_runs",
		Description: "List runs, newest first, with their contract version, status, and number of attempts.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runsInput) (*mcp.CallToolResult, any, error) {
		args := with("runs")
		if in.Contract != "" {
			args = append(args, "--contract", in.Contract)
		}
		if in.Limit != 0 {
			args = append(args, "--limit", strconv.Itoa(in.Limit))
		}
		return t.run(ctx, args...), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_run",
		Description: "Get a run: every attempt with each rule's result and reason, and for work runs the task, worker, and outcome.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getRunInput) (*mcp.CallToolResult, any, error) {
		args := with("show")
		if in.Attempt != 0 {
			args = append(args, "--attempt", strconv.Itoa(in.Attempt))
		}
		if in.Evidence {
			args = append(args, "--evidence")
		}
		if in.Run != "" {
			args = append(args, "--", in.Run)
		}
		return t.run(ctx, args...), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_evidence",
		Description: "Get the evidence recorded for a rule in an attempt, with its content: files, diffs, command output, and the exact request a reviewer was sent and its response.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in evidenceInput) (*mcp.CallToolResult, any, error) {
		if err := required("rule", in.Rule); err != nil {
			return toolError(err), nil, nil
		}
		args := with("evidence")
		if in.Attempt != 0 {
			args = append(args, "--attempt", strconv.Itoa(in.Attempt))
		}
		if in.Item != 0 {
			args = append(args, "--item", strconv.Itoa(in.Item))
		}
		args = append(args, "--")
		if in.Run != "" {
			args = append(args, in.Run)
		}
		return t.run(ctx, append(args, in.Rule)...), nil, nil
	})
	return server
}
