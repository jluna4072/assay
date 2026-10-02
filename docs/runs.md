# Persistent runs

`storage.Open(path)` opens a local SQLite database and initializes schema version 1. Drafts, immutable published versions, runs, attempts, and rule results are stored separately. A pure Go SQLite driver keeps backend checks independent of a desktop toolchain.

Create a service with `backend.NewService(store)`. `StartRun(contractID, version, projectDir)` only accepts a published version and an existing project directory. `EvaluateAttempt(runID)` evaluates its frozen rules and appends another attempt. `GetRun` returns the full history; `ListRuns` returns summaries.

Rule results distinguish `passed`, `failed`, `error`, and `blocked`. A failed prerequisite blocks its dependent rules. Operational errors block acceptance and are saved as errors rather than ordinary rule failures. Dependencies are evaluated before dependents even when the draft lists them in another order.

Attempt numbering, results, and run status are committed in one transaction. Rechecks never overwrite earlier attempts. Editing or publishing a new draft does not change an existing run's version. Published rows reject updates and deletes.

From `app/`, run `go test -race ./...`, `go vet ./...`, and `go build ./...`. Tests demonstrate missing-file failure, correction and a new passing attempt, retained history after reopening the database, version isolation, dependency blocking, operational errors, rollback, and concurrent rechecks.

Saved results are not evidence snapshots. File content/diff collection belongs to the later evidence phase.
