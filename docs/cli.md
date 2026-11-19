# Command line

`harness` is the command-line interface to the same Go backend the desktop app uses. Build or run it from `app/`:

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

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Negative verdict: a contract is invalid (later: a rule failed) |
| 2 | Operational error: a file or the database couldn't be read |
| 64 | Usage error: unknown command, bad flag, missing argument, no project found, unknown contract or version, publish without confirmation |

Flags may come before or after file names. Use `--` to pass a file name that starts with `-`.
