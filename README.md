# Harness

Local macOS prototype for contract-driven checks. Create a draft, publish immutable file-existence rules with dependencies, select a project, and inspect saved run attempts. React/TypeScript and React Flow provide the desktop view; Wails calls the shared Go service directly, and SQLite retains history.

## Run the desktop app

On macOS, install the Go version in `app/go.mod`, Node.js 24, and Xcode command line tools. From the repository root:

```sh
cd app
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
wails dev
```

`wails build` produces a local development app in `app/build/bin/`. Signing, notarization, and downloadable releases are a later phase.

Data is saved in `~/Library/Application Support/harness/harness.db` on macOS. Set `HARNESS_DATA_DIR` before launching to use a different directory. Credentials are not part of this prototype.

## Try the acceptance flow

1. Create a new draft with the default `readme` rule for `README.md`, then publish version 1.
2. Choose a project directory without `README.md` and click **Run checks**. Attempt 1 fails with a reason.
3. Create `README.md` in that directory, then click **Recheck project**. Attempt 2 passes while attempt 1 remains visible.
4. Close and reopen the app, then select the saved run. Both attempts remain available.
5. Edit the draft and publish another version. The earlier run stays tied to version 1.

## Validate

Backend checks run independently of the native desktop toolchain:

```sh
cd app
go test -race ./...
go vet ./...
go build ./...
```

Frontend type checks, production build, and browser acceptance tests:

```sh
cd app/frontend
npm ci
npm run build
npx playwright install chromium
npm test
```

Browser tests use a test-only localhost transport to the real Go/SQLite service and a substitute for the native directory dialog. They cover the visible failure/correction/history flow and publication validation. They do not launch a macOS window. CI builds the native macOS app on pushes to `main`; pull requests run only the Linux jobs.

`npm run dev` alone previews the UI; saving and checking require the Wails desktop app. The original single-check CLI demo remains available with `cd app && go run .`.

## Scope

Current checks support regular-file existence and acceptance dependencies. The graph is a preview; rules are edited with forms. Evidence snapshots, model reviewers, workers, automatic repairs, MCP access, graph drag-and-drop authoring, and releases remain future work.

See [contract publication](docs/contracts.md) and [persistent runs](docs/runs.md) for backend details.

## Roadmap

Work lands in small pull requests, each reviewed before the next one starts. 
1. **Core backend and basic UI.** Draft, validate, and publish versioned contracts (a graph of acceptance rules and their dependencies, not a prescribed coding order); persist runs and attempts in SQLite; add a basic desktop UI for contract forms, a graph preview, and run history.
2. **Evidence collection.** Collect file contents, diffs, and approved command results per attempt, and block a pass when required evidence is missing.
3. **Model review.** Evaluate subjective rules with a reviewer (Jev first, then user-selected local or cloud models); reviewer errors never produce a pass.
4. **Worker and repair loop.** Run a worker in a restricted workspace, return failures as repair context, and retry within attempt and time limits.
5. **MCP and visual authoring.** Let outside agents request tasks and check status through MCP, and upgrade the graph preview into an editor. Publishing stays under user control.
6. **Distribution.** Onboarding, plus signed and notarized macOS releases.

The first end-to-end milestone: an outside agent requests a task through MCP, harness rejects a flawed attempt, retries it, and the UI shows the evidence for the final decision.
