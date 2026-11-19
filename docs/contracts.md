# Contract publication

The Go backend exposes `SaveDraft`, `GetDraft`, `ListContracts`, `PublishContract`, `GetVersion`, and `ListVersions` through `backend.Service`. The command line and the future MCP server use the same service. `PublishContent` publishes a complete contract (such as one read from a file) and skips it when it matches the latest version.

Drafts can be incomplete. Publication requires a contract ID and name, at least one rule, and an ID, criterion, supported check kind, and project-relative path for every rule. The first check kind is `file_exists`. Dependencies must reference unique rule IDs and form an acyclic graph; rule order in the draft does not matter.

A publication freezes a snapshot under an increasing version number. Editing the draft or a returned value does not modify that snapshot. Repository implementations must allocate versions atomically and return independent snapshots. Storage is introduced in the next slice; service tests use a memory repository.

Rule dependencies describe acceptance prerequisites, not a required order for an agent to write code.

Run validation from `app/` with `go test ./...`, `go vet ./...`, and `go build ./...`. Tests cover incomplete drafts, invalid graphs, out-of-order valid graphs, version increments, and mutation isolation.
