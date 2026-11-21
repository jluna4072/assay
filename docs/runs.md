# Persistent runs

`storage.Open(path)` opens a local SQLite database and creates or upgrades it to schema version 2. Drafts, immutable published versions, runs, attempts, rule results, and evidence content are stored separately. Version 2 added each run's git baseline and a content-addressed `blobs` table for evidence; a version 1 database is upgraded in place when opened, and a database from a newer assay is refused. A pure Go SQLite driver keeps the build free of C toolchains.

Create a service with `backend.NewService(store)`. `StartRun(contractID, version, projectDir)` only accepts a published version and an existing project directory. `EvaluateAttempt(runID)` evaluates its frozen rules and appends another attempt. `GetRun` returns the full history; `ListRuns` returns summaries.

Rule results distinguish `passed`, `failed`, `error`, and `blocked`. A failed prerequisite blocks its dependent rules. Operational errors block acceptance and are saved as errors rather than ordinary rule failures. Dependencies are evaluated before dependents even when the draft lists them in another order.

Attempt numbering, results, and run status are committed in one transaction. Rechecks never overwrite earlier attempts. Editing or publishing a new draft does not change an existing run's version. Published rows reject updates and deletes.

From `app/`, run `go test -race ./...`, `go vet ./...`, and `go build ./...`. Tests demonstrate missing-file failure, correction and a new passing attempt, retained history after reopening the database, version isolation, dependency blocking, operational errors, rollback, and concurrent rechecks.

Evidence (`docs/cli.md`, "Evidence") is recorded with each rule result. Its content is stored in `blobs` under its SHA-256 in the same transaction as the attempt, so an attempt and its evidence are saved together or not at all, and identical content is stored once.
