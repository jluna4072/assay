# Getting started

This walks from installing assay to a first contract, a first check, and a first work run, in about ten minutes. [The command line guide](cli.md) covers everything in depth.

## 1. Install

```sh
brew install jluna4072/tap/assay
assay --version
```

On Linux, Homebrew works the same way, or download an archive from the [releases page](https://github.com/jluna4072/assay/releases) and put `assay` on your `PATH`. To build from source, see the [README](../README.md#install).

## 2. Set up a project

assay works in any folder; `assay work` also needs it to be a git repository.

```sh
cd ~/my-project
assay init
assay doctor
```

`init` creates:

- `assay/`, where your contracts live, committed with the project. It starts with `assay/example.yaml`.
- `.assay/`, your local state: `config.yaml` (the commands, reviewers, and workers you allow) and `assay.db` (published contracts and every run). It's added to `.gitignore`; only you edit it.

`assay doctor` checks git, the sandbox, and the project's setup, and prints a `fix:` line for anything missing. Run it again whenever something doesn't work.

## 3. Write a contract

A contract is a list of rules, each with a plain criterion and a way to check it. Save this as `assay/readme.yaml`:

```yaml
version: 1
id: readme
name: README with install steps
rules:
  - id: readme
    criterion: The project has a README
    check: file_exists
    path: README.md
  - id: install
    criterion: The README explains how to install
    check: command_succeeds
    command: has-install
    depends_on: [readme]
```

`install` runs a command, so you approve it first. Contracts name commands; only your `.assay/config.yaml` says what they run. Add this under `commands:` there:

```yaml
commands:
  has-install:
    run: [grep, -q, Install, README.md]
```

Approved commands run in the macOS sandbox: no network, and no writes outside a scratch folder, unless you allow them. Then validate the contract:

```sh
assay contract validate assay/readme.yaml
```

Rules can also have a model judge a criterion that a command can't (`check: review`), using Jev, Claude, OpenAI, or a local model; see [Reviewers](cli.md#reviewers).

## 4. Publish it

```sh
assay contract publish assay/readme.yaml
```

Publishing freezes the contract as `readme@1`. Checks always run against a published version, so the rules can't change while work is being judged. Edit the file and publish again to make `readme@2`; `assay contract diff assay/readme.yaml` shows what would change.

## 5. Check

```sh
assay check readme
```

Without a README, `readme` fails and `install` is blocked, because it depends on `readme`. Fix it and check again:

```sh
printf '# My project\n\n## Install\n\nRun make install.\n' > README.md
assay recheck
assay show
```

`recheck` makes attempt 2 of the same run, and `show` lists every attempt with the reason each rule passed or failed. Exit codes report the verdict (0 passed, 1 failed), so `assay check readme` works as a gate in scripts and CI.

## 6. Have a worker do the work

`assay work` gives a task to a worker (Codex, or any program), checks what it did, and sends back what failed until every rule passes or it runs out of attempts. To use Codex, install it, run `codex login`, and add it under `workers:` in `.assay/config.yaml`:

```yaml
workers:
  codex:
    run: [codex, exec, --sandbox, workspace-write, --ephemeral, --color, never, "-"]
    timeout: 20m
    sandbox: off   # Codex applies its own sandbox
```

Commit your work first; the worker starts from the last commit. Then:

```sh
git add -A && git commit -m "Add the readme contract"
assay work readme --task "Write a README with install steps"
```

The worker runs in its own git worktree on a branch named `assay/<run>`, so your checkout isn't touched. When it's accepted, review the branch and merge it yourself; assay never merges. `assay show --evidence` shows each attempt's prompt, the worker's output, and the evidence behind every verdict.

## 7. Browse it all

```sh
assay
```

Typing `assay` at a terminal opens a full-screen view of your contracts, runs, and evidence. Press `v` to validate, `p` to publish, `c` to check, `r` to recheck, `w` to start work, and `?` for every key. Work runs show live as they happen.

## 8. Use it from Claude Code

```sh
claude mcp add --scope user assay -- assay mcp
```

Claude Code can then list and validate contracts, check its own work, read why rules failed, and start work runs. It can't publish a contract; that stays with you. See [Use it with Claude Code](../README.md#use-it-with-claude-code) for a `CLAUDE.md` snippet and example prompts.

## Next

- Group contracts into a [suite](cli.md#suites) you can add to any project.
- Record [evidence](cli.md#evidence) such as file contents, diffs, and command output with each check.
- Set up a [reviewer](cli.md#reviewers) for criteria a command can't check.
