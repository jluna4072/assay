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
| `rules[].check` | How the rule is checked; `file_exists` is the only check so far |
| `rules[].path` | Project-relative path for `file_exists` |
| `rules[].depends_on` | Rules that must pass before this one is checked |

Decoding is strict. Unknown keys (such as a misspelled `depend_on`), duplicate keys, wrong types, and a second YAML document in the same file are reported as problems instead of being ignored.

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

Creates `.harness/harness.db` and `harness/example.yaml` (unless `harness/` already holds contract files). Inside a git repository it adds `/.harness/` to `.gitignore`. Running it again changes nothing that already exists.

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

Run `status` is `ready` (no attempts yet), `passed`, `failed`, or `error`, from the latest attempt. `results` keep the contract's rule order. `app/cli/testdata/golden/` holds the exact shapes the tests enforce.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success; for `check` and `recheck`, every rule passed |
| 1 | Negative verdict: a contract is invalid, or a rule failed or was blocked |
| 2 | Operational error: a file or the database couldn't be read, or a check couldn't run |
| 64 | Usage error: unknown command, bad flag, missing argument, no project found, unknown contract, version, or run, publish without confirmation |

Exit codes make `harness check` usable as a gate in scripts and CI: `harness check docs && deploy`.

Flags may come before or after file names. Use `--` to pass a file name that starts with `-`.
