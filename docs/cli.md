# Command line

`harness` is the command-line interface to the same Go backend the desktop app uses. Build or run it from `app/`:

```sh
go run ./cmd/harness --help
go install ./cmd/harness   # puts harness in $(go env GOPATH)/bin
```

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

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Negative verdict: a contract is invalid (later: a rule failed) |
| 2 | Operational error: a file couldn't be read |
| 64 | Usage error: unknown command, bad flag, missing argument |

Flags may come before or after file names. Use `--` to pass a file name that starts with `-`.
