# Harness

A command-line harness for contract-driven checks of agent work. You publish a contract (acceptance rules and the dependencies between them) as a frozen version; harness checks a project against it, records every attempt and why each rule passed or failed, and keeps earlier attempts when you fix things and check again.

## Install

Install the Go version named in `app/go.mod`, then from the repository root:

```sh
cd app
go install ./cmd/harness   # puts harness in $(go env GOPATH)/bin
```

## Try it

```sh
cd ~/my-project
harness init                                   # creates .harness/ and harness/example.yaml
harness contract validate harness/example.yaml
harness contract publish harness/example.yaml  # freezes example@1
harness check example                          # fails if there's no README.md
touch README.md
harness recheck                                # passes; attempt 1's failure is kept
harness show                                   # every attempt, with reasons
```

Contracts are YAML files in `harness/`, committed with the project. Published versions, runs, and attempts live in `.harness/harness.db`, which `init` adds to `.gitignore`. Every command accepts `--json`, and exit codes report the verdict (0 passed, 1 failed, 2 couldn't run, 64 usage error), so `harness check` works as a gate in scripts and CI.

See [the command line guide](docs/cli.md) for the contract format, every command, and the JSON output.

## Develop

From `app/`:

```sh
gofmt -l .
go vet ./...
go test -race ./...
go build ./...
```

Tests need no network, credentials, or external tools. `go test ./cli -update` regenerates the JSON golden files in `app/cli/testdata/golden/` when an output change is intended.

| Package | Role |
|---|---|
| `cmd/harness` | Entry point |
| `cli` | Commands, flags, output, exit codes |
| `contractfile` | Strict YAML contract loading with line-numbered problems |
| `project` | Finding a project's `.harness/` directory |
| `backend` | Contracts, validation, publishing, runs, and checks |
| `storage` | SQLite persistence |

Backend details: [contract publication](docs/contracts.md) and [persistent runs](docs/runs.md).

## Scope

Rules can check that a file exists or that an approved command succeeds, with dependencies between them. Each attempt can record file contents, git diffs, and command output as evidence; commands run under the macOS sandbox. Model reviewers, workers, automatic repairs, MCP access, and releases are future work.

## Roadmap

Work lands in small pull requests, each reviewed before the next one starts.

1. **Core command line.** Contract files, validation, publishing, runs, and attempts in SQLite; a text view of the dependency graph.
2. **Evidence collection.** Collect file contents, diffs, and user-approved command results per attempt, and block a pass when required evidence is missing.
3. **Model review.** Evaluate subjective rules with a reviewer (Jev first, then user-selected local or cloud models); reviewer errors never produce a pass.
4. **Worker and repair loop.** `harness work` runs a worker in a restricted workspace, returns failures as repair context, and retries within attempt and time limits.
5. **MCP.** `harness mcp` lets outside agents draft contracts, request tasks, and check status. Publishing stays under user control.
6. **Distribution.** Release binaries and a Homebrew tap, plus `harness doctor` for setup.

The first end-to-end milestone: an outside agent requests a task through MCP, harness rejects a flawed attempt, retries it, and `harness show` presents the evidence for the final decision.
