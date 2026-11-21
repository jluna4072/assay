# Command line

`harness` is the command-line interface to the Go backend in `app/backend`. Build or run it from `app/`:

```sh
go run ./cmd/harness --help
go install ./cmd/harness   # puts harness in $(go env GOPATH)/bin
```

## Projects

A harness project is a directory with two folders:

| Folder | Holds | In git? |
|---|---|---|
| `harness/` | Contract files you write and review | Yes |
| `.harness/` | `harness.db`: published versions and, later, runs | No (gitignored) |

`harness init` creates both. Every other project command finds the project by walking up from the working directory to the nearest `.harness/`, the way git finds `.git`. `--project <dir>` uses the project rooted exactly at `<dir>` instead.

## Contract files

A contract is one YAML file:

```yaml
version: 1            # optional file format marker; only 1 is supported
id: docs
name: Documentation
rules:
  - id: readme
    criterion: Project has a README
    check: file_exists
    path: README.md
  - id: guide
    criterion: Project has a guide
    check: file_exists
    path: docs/guide.md
    depends_on: [readme]
```

| Key | Meaning |
|---|---|
| `id`, `name` | Contract identifier and display name |
| `rules[].id` | Unique rule identifier |
| `rules[].criterion` | The acceptance criterion in plain words |
| `rules[].check` | How the rule is checked: `file_exists`, `command_succeeds`, or `review` |
| `rules[].path` | Project-relative path, for `file_exists` |
| `rules[].command` | Name of an approved command, for `command_succeeds`: the rule passes when it exits with status 0 |
| `rules[].reviewer` | For `review`: which configured reviewer judges the rule. Without it, the project's default reviewer does. |
| `rules[].depends_on` | Rules that must pass before this one is checked |
| `rules[].evidence` | What harness records when it checks the rule (below) |

### Evidence

A rule can ask harness to record evidence each time it's checked, so every decision has an inspectable record of exactly what harness looked at:

```yaml
  - id: guide
    criterion: Project has a guide
    check: file_exists
    path: docs/guide.md
    evidence:
      - file: docs/guide.md        # the file's contents
      - diff: {}                   # every change since the run started
      - diff: {paths: [docs]}      # changes under docs/ only
```

| Item | Records |
|---|---|
| `file: <path>` | The file's contents, or that it was absent |
| `diff: {}` | `git diff` from the commit the project was at when the run started to the files as they are now, including staged, unstaged, and untracked files (not ignored ones, and never `.harness/`). The project's own git index isn't touched. |
| `diff: {paths: [...]}` | The same, limited to those paths |
| `command: <name>` | An approved command's combined output, exit status, and duration. A `command_succeeds` rule records its own command automatically. |

Evidence is collected together with the check, from the same project state, and stored in `.harness/harness.db`, so later edits never change what an earlier attempt recorded. Each item is capped at 1 MiB; larger content is stored up to the cap and marked truncated, with its full size and SHA-256 recorded. Evidence is collected for failed rules too, but not for blocked ones, which aren't checked.

Each command runs at most once per attempt, however many rules ask for it.

**Missing evidence can't produce a pass.** If an item can't be collected (a path is a directory, a symlink leads outside the project, or diff evidence is requested in a project that isn't a git repository), a rule that would have passed is marked `error` instead. An absent file isn't a collection failure: "the file didn't exist" is recorded as the evidence.

Decoding is strict. Unknown keys (such as a misspelled `depend_on`), duplicate keys, wrong types, and a second YAML document in the same file are reported as problems instead of being ignored.

## Approved commands

Contracts can't run arbitrary commands. They name commands you approve in `.harness/config.yaml`, which `harness init` creates. The file lives in `.harness/`, so it isn't committed, and agents and contract authors can't change what a name runs.

```yaml
version: 1
sandbox: on
commands:
  go-test:
    run: [go, test, ./...]   # program and arguments; no shell
    dir: app                 # where to run it, relative to the project (default: the root)
    timeout: 5m              # default 5m, at most 1h; a timeout fails the rule
    network: off             # off (default), localhost, or on
    writes: scratch          # scratch (default) or project
    env: [GOFLAGS]           # variables passed through from your environment
    caches: [GOCACHE]        # variables pointed at .harness/cache/go-test/<NAME>
```

```yaml
  - id: tests
    criterion: The test suite passes
    check: command_succeeds
    command: go-test
```

`contract validate` (inside a project) and `contract publish` reject contracts that name commands the config doesn't approve.

### The sandbox

On macOS, approved commands run under the system sandbox (`sandbox-exec`). For a command and everything it starts:

- **No network** unless the command allows `localhost` (connecting to and listening on local ports) or `on`.
- **Writes only to a scratch directory** (`TMPDIR`, emptied after each attempt) and the command's declared caches (kept between attempts). With `writes: project` it may also write anywhere in the project, but never in `.harness/`.
- **No reading** `~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.docker`, `~/.config/gh`, `~/.netrc`, the keychains, or harness's database.
- **A cleared environment**: `PATH`, `HOME`, `LANG`, `TMPDIR`, `XDG_CACHE_HOME`, plus the variables in `env` and `caches`. Nothing else from your environment is passed.
- **A timeout** that stops the command and every process it started.

Toolchains that write caches outside the project need them declared. For Go, `caches: [GOCACHE]`; downloaded modules in `~/go/pkg/mod` are readable, but new downloads need `network: on`.

If the sandbox isn't available (on Linux, for example), commands can't run and their rules report `error`. `sandbox: off` runs them without it; their evidence is then marked unsandboxed (`unsandboxed` in `show --evidence`, `"sandboxed": false` in JSON).

A command with `writes: project` changes the project for rules checked after it in the same attempt. Prefer scratch writes; see `docs/spikes/command-sandbox.md` for how the sandbox was tested.

## Reviewers

Some criteria can't be checked by a program ("the README explains how to install"). A `review` rule sends its criterion and evidence to a reviewer, usually a model, which answers `pass`, `fail`, or `insufficient_evidence`:

```yaml
  - id: readme-explains-install
    criterion: README explains how to install and run the project
    check: review
    reviewer: careful          # optional; without it, the default reviewer
    evidence:
      - file: README.md
```

A review rule needs at least one evidence item: the reviewer sees exactly what harness recorded, never the project itself.

Reviewers are configured in `.harness/config.yaml`, next to approved commands, so each person chooses the model that reviews their work and a committed contract works with any of them:

```yaml
reviewers:
  careful:
    protocol: command
    run: [my-reviewer]       # program and arguments; no shell
    timeout: 2m              # default 2m, at most 1h
    network: on              # off (default), localhost (a local model), or on
    env: [MY_API_KEY]        # variables passed through from your environment
default_reviewer: careful    # needed only with more than one reviewer
```

With one reviewer, it's the default. `contract validate` (inside a project) and `contract publish` reject review rules whose reviewer isn't configured.

Three protocols are supported:

- `typesafe`: TypeSafe's Jev, called directly with a TypeSafe console key (below).
- `decisions`: a decision model through an OpenAI-compatible Decisions API, such as Jev through Vercel's AI Gateway with a Vercel key (below).
- `command`: a program that reads a review request on stdin and writes its verdict on stdout, both JSON. It runs like an approved command (sandbox, cleared environment, timeout), except it can never write to the project.

Built-in protocols for OpenAI-compatible chat servers (including local ones such as Ollama) and Anthropic's API are planned.

### Jev and other decision models

With a key from TypeSafe's console:

```yaml
reviewers:
  jev:
    protocol: typesafe
    endpoint: https://api.typesafe.ai/v1   # default
    model: jev-latest                      # default
    threshold: 0.9                         # default: probability needed to pass
    key_env: TYPESAFE_API_KEY              # default
    timeout: 2m                            # default
```

harness sends `POST <endpoint>/systemone` with the evidence as the `state` and one yes/no (`noul`) question: is this criterion met?

With a key from Vercel's AI Gateway, or another service that offers an OpenAI-compatible Decisions API:

```yaml
reviewers:
  jev:
    protocol: decisions
    endpoint: https://ai-gateway.vercel.sh/v1   # default
    model: typesafe-ai/jev                       # default
    threshold: 0.9                               # default: probability needed to pass
    key_env: AI_GATEWAY_API_KEY                  # default
    timeout: 2m                                  # default
```

harness sends `POST <endpoint>/decisions` with the rule's evidence as JSON input and one yes/no (`predicate`) question.

Keys are specific to the service that issued them: a TypeSafe key works only with `typesafe`, and a Vercel key only with Vercel's endpoint. A key sent to the wrong service is rejected (`api.typesafe.ai rejected the API key (HTTP 401)`), and the message names the service so the mismatch is clear.

Either way, the model answers with the probability that it is. The rule passes when that probability reaches `threshold`; anything lower fails, with the probability in the reason (`jev: probability 0.62 that the criterion is met; passing needs 0.90`). Decision models give no written explanation. A refusal, an answer that isn't a single predicate with a probability between 0 and 1, an HTTP error, or a timeout is an `error`. The endpoint must use https unless it's on this machine. Requests larger than 128 KiB (about the 32,000 tokens Jev reads) aren't sent; the rule is an `error`.

Unlike `command` reviewers, harness makes these requests itself, outside the sandbox, and only to the configured endpoint.

### API keys

A `typesafe` or `decisions` reviewer's key comes from, in order:

1. The variable named by `key_env`, when it's set (for CI, or to override).
2. Your macOS Keychain, where harness saves keys per API host, so reviewers on the same provider share one.
3. Nothing, for an endpoint on this machine (a local server).

**You set a key up once.** The first time a review needs a key that isn't set up, and you're at a terminal, harness asks for it (input is hidden), saves it to the Keychain, and carries on with the check:

```
$ harness check docs
Reviewer jev needs an API key for api.typesafe.ai, and none is set up yet.
Paste the key (input is hidden):
Saved to your Keychain; harness won't ask again on this Mac.
```

harness never asks when there's no one to answer: when stdin isn't a terminal, or with `--json`. The rule is then an `error` that says how to provide a key. Keys are never written to config, contracts, logs, evidence, or the database; the recorded request doesn't include the `Authorization` header. harness stores keys through Apple's `security` tool, passing the key on its stdin rather than as an argument.

A rejected key (HTTP 401 or 403) is an `error` that says to replace it with `harness reviewer login <name>`.

### The review protocol

harness sends one request per rule:

```json
{
  "protocol": 1,
  "rule": {"id": "readme-explains-install", "criterion": "README explains how to install and run the project"},
  "evidence": [
    {"item": 1, "kind": "file", "path": "README.md", "size": 31, "sha256": "…", "content": "# Demo\n\nRun make install.\n"}
  ]
}
```

Evidence items carry what `show --evidence` lists (`kind`, `path` or `paths`, `baseline`, `command`, `exitCode`, `timedOut`, `size`, `sha256`) and their content: text in `content`, anything that isn't UTF-8 in `contentBase64`. An absent file is `"absent": true` with no content.

The reviewer prints one JSON object:

```json
{
  "protocol": 1,
  "verdict": "fail",
  "explanation": "The README describes features but has no install steps.",
  "cites": [1],
  "model": "my-model-2",
  "probability": 0.08
}
```

| Field | Meaning |
|---|---|
| `protocol` | Required; `1` |
| `verdict` | Required: `pass`, `fail`, or `insufficient_evidence` |
| `explanation` | The reviewer's reasoning, shown as the rule's reason and stored verbatim |
| `cites` | Evidence item numbers the verdict relies on |
| `model` | Which model answered, for the record |
| `probability` | Optional, 0 to 1: how likely the criterion is met, for reviewers that estimate it |

Other fields are ignored. **Only `pass` passes**; `insufficient_evidence` fails the rule. Everything else is an `error`, never a pass: a non-zero exit, a timeout, output that isn't exactly one JSON object, a missing or unknown verdict, a citation of an item that wasn't sent, a probability outside 0 to 1, or more than 1 MiB of output. A rule isn't sent for review at all if any of its evidence couldn't be collected or was truncated.

Each review is recorded with the rule: the reviewer, its verdict, explanation, citations, model, and duration, plus the exact request and response as evidence items after the rule's own (`review request to careful`, `review response from careful`), so `harness evidence <rule> --item <n>` shows exactly what the reviewer saw and said.

## Commands

### `harness contract validate <file>...`

Reports every problem that would stop each file from being published, with line numbers, and saves nothing.

```
$ harness contract validate harness/docs.yaml
✗ harness/docs.yaml: 2 problems
  harness/docs.yaml:13: unknown key "depend_on"
  harness/docs.yaml:14:5: rule "guide": dependency cycle readme → guide → readme
```

`--json` prints one JSON object instead:

```json
{
  "schema": 1,
  "valid": false,
  "files": [
    {
      "path": "harness/docs.yaml",
      "valid": false,
      "problems": [
        { "line": 13, "message": "unknown key \"depend_on\"" },
        { "line": 14, "column": 5, "rule": "guide", "message": "dependency cycle readme → guide → readme" }
      ]
    }
  ]
}
```

`schema` changes only when the JSON shape changes incompatibly. Valid files include `"contract": {"id", "name", "rules"}`; unreadable files include `"error"`.

### `harness init [dir]`

Creates `.harness/harness.db`, `.harness/config.yaml` (approved commands; see above), and `harness/example.yaml` (unless `harness/` already holds contract files). Inside a git repository it adds `/.harness/` to `.gitignore`. Running it again changes nothing that already exists.

```
$ harness init
created  .harness/harness.db
created  harness/example.yaml
added    /.harness/ to .gitignore
Initialized harness project in /Users/me/my-project
Next: harness contract validate harness/example.yaml
```

### `harness contract publish <file>`

Validates the file and stores it as the contract's next version. Published versions never change, and later runs stay tied to the version they started with. Publishing a file that matches the latest version stores nothing:

```
$ harness contract publish harness/docs.yaml
published docs@2 (2 rules)
$ harness contract publish harness/docs.yaml
docs@2 unchanged: the file matches the latest published version
```

Publishing is the contract owner's decision. Without a terminal on stdin (a script, CI, or an agent running shell commands), `publish` refuses with exit 64 unless `--yes` is passed. An invalid file is reported like `validate` and exits 1.

### `harness contract versions <id>`

Lists published versions, newest first.

```
$ harness contract versions docs
docs (Documentation)
  @2   2026-11-19 09:19  2 rules  latest
  @1   2026-11-18 21:04  1 rule
```

### `harness contract show <id>[@version]`

Prints a published version in the contract file format, so it can be diffed or saved. `docs` and `docs@latest` mean the latest version.

An unknown contract or version is a usage error (exit 64). All three contract commands accept `--json` and `--project <dir>`.

### `harness contract graph <id>[@version] | <file>`

Draws the rules and their dependencies, starting from the rules nothing else depends on, with each rule's dependencies beneath it. A rule reached a second time is drawn once and then marked `(see above)`. An argument containing `/` or ending in `.yaml`/`.yml` is read as a contract file, so a draft can be drawn before it's published.

```
$ harness contract graph harness/release.yaml
harness/release.yaml (Release readiness, unpublished)
notes — Release notes say "ready"
├── guide — Guide exists
│   └── readme — Project has a README
└── changelog — Changelog exists
    ├── readme — Project has a README
    └── license — Project has a LICENSE
```

`--run <run>` marks each rule with its result in that run's latest attempt and shows the reason for anything that didn't pass:

```
$ harness contract graph release --run wjweu7
release@1 (Release readiness) · run wjweu7, attempt 1
⊘ notes — blocked: dependency guide did not pass
├── ✗ guide — docs/guide.md does not exist
│   └── ✓ readme — Project has a README
└── ⊘ changelog — blocked: dependency license did not pass
    ├── ✓ readme — Project has a README
    └── ✗ license — LICENSE does not exist
```

`--format mermaid` prints a Mermaid flowchart (GitHub renders these in Markdown), and `--format dot` prints a Graphviz digraph; both color rules by result with `--run`. `--json` prints the rules, their dependencies, the roots, and (with `--run`) each rule's status and reason.

```sh
harness contract graph release --run wjweu7 --format dot | dot -Tsvg > release.svg
```

### `harness check <id>[@version]`

Starts a run of a published version against the project and records attempt 1. The run stays tied to that version even after newer versions are published.

```
$ harness check docs
docs@1 · run ijyvmk · attempt 1 · FAILED

  ✗ readme   README.md does not exist
  ⊘ guide    blocked: dependency readme did not pass

1 failed, 1 blocked · fix, then: harness recheck ijyvmk
```

Each rule ends in one of four states. Rules needing attention are listed first.

| Mark | Status | Meaning |
|---|---|---|
| ✓ | `passed` | The check succeeded |
| ✗ | `failed` | The check ran and the rule isn't met |
| ⊘ | `blocked` | A rule it depends on didn't pass, so it wasn't checked |
| ! | `error` | The check couldn't run (for example, a path escapes the project) |

### `harness recheck [run]`

Adds an attempt to a run after you've changed the project. Earlier attempts are kept. Without a run ID it uses the most recent run.

### `harness runs`

Lists runs, newest first. `--contract <id>` filters by contract; `--limit <n>` shows at most n (default 20, `0` for all).

```
$ harness runs
RUN     CONTRACT  STATUS  ATTEMPTS  STARTED
ijyvmk  docs@1    passed  3         2026-11-19 09:49
```

### `harness show [run]`

Prints every attempt of a run, oldest first, with each rule's reason. Without a run ID it shows the most recent run; `--attempt <n>` shows one attempt.

```
$ harness show
run ijyvmk · docs@1 · /Users/me/my-project
started 2026-11-19 09:49 · passed on attempt 3

attempt 1 · failed · 2026-11-19 09:49:58
  ✗ readme   README.md does not exist
  ⊘ guide    blocked: dependency readme did not pass

attempt 2 · failed · 2026-11-19 09:50:31
  ✗ guide    docs/guide.md does not exist
  ✓ readme

attempt 3 · passed · 2026-11-19 09:51:02
  ✓ readme
  ✓ guide
```

### `harness show --evidence`

Adds a numbered line for each evidence item under its rule, and a summary line for reviews:

```
attempt 2 · passed · 2026-11-19 10:47:46
  ✓ readme
      [1] file README.md · 7 B · sha256 31ca6c61ca3f
  ✓ guide
      [1] file docs/guide.md · 15 B · sha256 a463a755151d
      [2] diff since d68d072 of docs · 1 file changed, 151 B · sha256 9f5507d52344
```

In a git repository, the header also shows the commit the run started at (`started 2026-11-19 10:47 at d68d072`).

### `harness evidence [run] <rule>`

Prints a rule's recorded evidence from the latest attempt (`--attempt <n>` for another), each item under a `==> … <==` header:

```
$ harness evidence guide
==> [1] file docs/guide.md · 15 B · sha256 a463a755151d <==
How to use it.

==> [2] diff since d68d072 of docs · 1 file changed, 151 B · sha256 9f5507d52344 <==
diff --git a/docs/guide.md b/docs/guide.md
new file mode 100644
...
```

`--item <n>` prints only that item's content, with no header, for piping or saving: `harness evidence guide --item 1 > guide-at-attempt-2.md`. `--json` includes each item's content (as `content`, or `contentBase64` for binary data). It exits 1 if a selected item is an absent file and 2 if one couldn't be collected.

### `harness reviewer list`

Lists configured reviewers, what each uses, which is the default, and where each API reviewer's key comes from (`$TYPESAFE_API_KEY`, `keychain`, `not needed`, or `missing`). It never prints a key.

### `harness reviewer login [name]` and `harness reviewer logout [name]`

`login` asks for a reviewer's API key (the default reviewer without a name) and saves it to your Keychain, replacing any saved key; you only need it to set a key up ahead of time or replace one. With stdin not a terminal, it reads the key from stdin: `printf %s "$KEY" | harness reviewer login jev`. `logout` removes the saved key for the reviewer's API host. Command reviewers get keys from the variables listed under `env:` instead.

### `harness reviewer test [name]`

Sends a reviewer (the default without a name) two fixed samples, one whose evidence meets the criterion and one whose evidence doesn't, and reports how it judged each. Exits 0 when it passed the first and failed the second, 1 when it judged one wrongly, and 2 when it couldn't answer. Nothing is recorded.

```
$ harness reviewer test careful
careful · command protocol · my-reviewer

  ✓ sample that meets the criterion (should pass)
      pass · model my-model-2 · cites 1 · 1.2s: It greets the reader.
  ✓ sample that doesn't meet the criterion (should fail)
      fail · model my-model-2 · cites 1 · 1.1s: It says goodbye, not hello.

careful works: it passed the sample that meets the criterion and failed the one that doesn't.
```

### Run IDs

Runs are shown by the first six characters of their ID. Anywhere a run ID is accepted, any unique prefix works, in either case. An ambiguous prefix lists the matching runs and exits 64.

### Run JSON

`check`, `recheck`, and `show` accept `--json` and print the run in this shape (`check` and `recheck` add `"attempt"`, the number of the attempt just recorded):

```json
{
  "schema": 1,
  "attempt": 1,
  "run": {
    "id": "ijyvmkq3ccxnmkoq7u3bbomxnb",
    "shortId": "ijyvmk",
    "contract": "docs",
    "version": 1,
    "projectDir": "/Users/me/my-project",
    "status": "failed",
    "createdAt": "2026-11-19T16:49:58.976127Z",
    "attempts": [
      {
        "number": 1,
        "status": "failed",
        "startedAt": "2026-11-19T16:49:58.976634Z",
        "completedAt": "2026-11-19T16:49:58.976687Z",
        "results": [
          { "rule": "readme", "status": "failed", "reason": "README.md does not exist" },
          { "rule": "guide", "status": "blocked", "reason": "dependency readme did not pass" }
        ]
      }
    ]
  }
}
```

Run `status` is `ready` (no attempts yet), `passed`, `failed`, or `error`, from the latest attempt. In a git repository, the run has a `baseline` (the commit it started at). Rules with evidence have an `evidence` list with each item's `kind`, `path` or `paths`, `size`, `sha256`, and, when they apply, `baseline`, `files` (diffs), `absent`, `truncated`, and `error`. Command items add `command`, `argv`, `exitCode`, `durationMs`, `sandboxed`, and `timedOut`; review request and response items add `reviewer`. Review rules that reached a reviewer have a `review` object: `reviewer`, `protocol`, `verdict`, `explanation`, `cites`, `model`, `probability`, `requestSha256` (the hash of the exact request), `durationMs`, `sandboxed`, and `error` when there's no verdict. `results` keep the contract's rule order. `app/cli/testdata/golden/` holds the exact shapes the tests enforce.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success; for `check` and `recheck`, every rule passed |
| 1 | Negative verdict: a contract is invalid, or a rule failed or was blocked |
| 2 | Operational error: a file or the database couldn't be read, or a check couldn't run |
| 64 | Usage error: unknown command, bad flag, missing argument, no project found, unknown contract, version, or run, publish without confirmation |

Exit codes make `harness check` usable as a gate in scripts and CI: `harness check docs && deploy`.

Flags may come before or after file names. Use `--` to pass a file name that starts with `-`.
