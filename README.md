# Assay

assay is a command-line tool that checks agent work against contracts and proves each verdict with evidence. You publish a contract (acceptance rules and the dependencies between them) as a frozen version; assay checks a project against it, records every attempt and why each rule passed or failed, and keeps earlier attempts when you fix things and check again.

## Install

Install the Go version named in `app/go.mod`, then from the repository root:

```sh
cd app
go install ./cmd/assay   # puts assay in $(go env GOPATH)/bin
```

## Try it

```sh
cd ~/my-project
assay init                                   # creates .assay/ and assay/example.yaml
assay contract validate assay/example.yaml
assay contract publish assay/example.yaml  # freezes example@1
assay check example                          # fails if there's no README.md
touch README.md
assay recheck                                # passes; attempt 1's failure is kept
assay show                                   # every attempt, with reasons
assay                                        # browse, publish, and check in a full-screen UI
```

Contracts are YAML files in `assay/`, committed with the project. Published versions, runs, and attempts live in `.assay/assay.db`, which `init` adds to `.gitignore`. Every command accepts `--json`, and exit codes report the verdict (0 passed, 1 failed, 2 couldn't run, 64 usage error), so `assay check` works as a gate in scripts and CI.

See [the command line guide](docs/cli.md) for the contract format, every command, and the JSON output.

## Use it with Claude Code

assay works as Claude Code's referee: Claude writes contracts, asks you to publish them, checks its own work, reads why rules failed, and can hand tasks to workers. It can't publish a contract or decide on its own that it's done.

**1. Add the MCP server once.** It finds the project from the folder Claude Code is running in, so one entry covers every project you've run `assay init` in:

```sh
claude mcp add --scope user assay -- assay mcp
```

(Use `claude mcp add assay -- assay mcp` inside a project to add it to that project only.)

**2. Set up each project.** Run `assay init`, then approve the commands and configure the reviewers your contracts use in `.assay/config.yaml` (see [the guide](docs/cli.md)). To reuse a group of contracts, `assay suite add <name>`.

**3. Tell Claude how you work.** Add this to the project's `CLAUDE.md` (or `~/.claude/CLAUDE.md` for every project):

```markdown
## Definition of done: assay

This project uses assay (the `assay` MCP server) to decide when work is done.

- Before starting, find the contract for the task with `list_contracts` and read it with `get_contract`. If none fits, write one in `assay/<id>.yaml` (or a suite in `assay/suites/<id>/`), run `validate_contract` until it has no problems, show me `diff_contract`, and ask me to publish it. Never publish it yourself.
- Write rules assay can check: `file_exists`, `command_succeeds` with commands approved in `.assay/config.yaml`, and `review` rules that list the evidence a reviewer needs (files, diffs, command output) for anything a command can't judge.
- After making changes, run `check` the first time and `recheck` after that. When a rule fails, read its reason and `get_evidence`, fix it, and recheck. Don't change the contract to make it pass.
- Don't tell me the work is done until `check` or `recheck` reports every rule passed, and give me the run ID.
- To hand a task to a configured worker instead, use `start_work` and follow it with `get_run` and `wait_seconds`; tell me the branch to review. Nothing is merged for me.
```

**4. Ask for work as usual.** For example: "Add install steps to the README, and write an assay contract that checks they're clear", or "Use assay to have the codex worker add tests until the `tests` contract passes." Publish contracts when Claude asks, with `assay contract publish <file>` or `p` in `assay`; watch runs in `assay` or with `assay show --follow`.

## Develop

From `app/`:

```sh
gofmt -l .
go vet ./...
go test -race ./...
go build ./...
```

Tests need no network, credentials, or external tools; Keychain tests use a throwaway keychain, never yours. `go test ./cli -update` regenerates the JSON golden files in `app/cli/testdata/golden/` when an output change is intended.

| Package | Role |
|---|---|
| `cmd/assay` | Entry point |
| `cli` | Commands, flags, output, exit codes |
| `contractfile` | Strict YAML contract loading with line-numbered problems |
| `project` | Finding a project's `.assay/` directory |
| `backend` | Contracts, validation, publishing, runs, and checks |
| `storage` | SQLite persistence |
| `keychain` | Saving reviewer API keys in the macOS Keychain |

Backend details: [contract publication](docs/contracts.md) and [persistent runs](docs/runs.md).

## Scope

Contracts can be grouped into suites, folders you keep in a library and add to any project, that pass only when every rule of every contract passes. Rules can check that a file exists, that an approved command succeeds, or that a reviewer you configure (TypeSafe's Jev, a Claude or OpenAI model, a model running locally through Ollama, or any program that speaks assay's review protocol) judges the evidence to meet a criterion, with dependencies between them. Each attempt can record file contents, git diffs, and command output as evidence, plus the exact review request and response; commands and reviewers run under the macOS sandbox. `assay work` has a worker (an agent or any program) do a task in a git worktree, checks the result, and sends back what failed until every rule passes or a limit is reached; the result is a branch you review and merge. Typing `assay` at a terminal opens a full-screen view for browsing contracts, runs, and evidence, for validating, publishing, editing, checking, and rechecking with a key, and for starting work runs and watching them, wherever they were started, as they happen. `assay mcp` lets an agent such as Claude Code list and validate contracts, check its work, and read why rules failed, without being able to publish. Releases are future work.

## Roadmap

Work lands in small pull requests, each reviewed before the next one starts.

1. **Core command line.** Contract files, validation, publishing, runs, and attempts in SQLite; a text view of the dependency graph.
2. **Evidence collection.** Collect file contents, diffs, and user-approved command results per attempt, and block a pass when required evidence is missing.
3. **Model review.** Evaluate subjective rules with a reviewer the user chooses: any program speaking assay's review protocol, then built-in support for decision models such as TypeSafe's Jev, OpenAI-compatible and local servers, and Anthropic's API. Reviewer errors never produce a pass.
4. **Worker and repair loop.** `assay work` runs a worker in a restricted workspace, returns failures as repair context, and retries within attempt and time limits.
5. **Terminal UI.** `assay` at a terminal opens a full-screen view of contracts, runs, and evidence, then gains actions and live work runs.
6. **MCP and Claude Code.** `assay mcp` lets an agent validate contracts, check its work, read runs and evidence, and start and cancel work runs; the README shows how to set it up in Claude Code. Publishing stays under user control.
7. **Distribution.** Release binaries and a Homebrew tap, plus `assay doctor` for setup.

The first end-to-end milestone: an outside agent requests a task through MCP, assay rejects a flawed attempt, retries it, and `assay show` presents the evidence for the final decision.
