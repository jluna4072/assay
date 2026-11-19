# Harness

Local prototype for contract-driven agent workflows. The current Go proof of concept lives in `app/`.

Run it with `cd app && go run .`.

## Roadmap

Work lands in small pull requests, each reviewed before the next one starts. The prototype currently returns a pass/fail result with a failure reason from a deterministic file-exists check.

1. **Core backend and basic UI.** Draft, validate, and publish versioned contracts (a graph of acceptance rules and their dependencies, not a prescribed coding order); persist runs and attempts in SQLite; add a basic desktop UI for contract forms, a graph preview, and run history.
2. **Evidence collection.** Collect file contents, diffs, and approved command results per attempt, and block a pass when required evidence is missing.
3. **Model review.** Evaluate subjective rules with a reviewer (Jev first, then user-selected local or cloud models); reviewer errors never produce a pass.
4. **Worker and repair loop.** Run a worker in a restricted workspace, return failures as repair context, and retry within attempt and time limits.
5. **MCP and visual authoring.** Let outside agents request tasks and check status through MCP, and upgrade the graph preview into an editor. Publishing stays under user control.
6. **Distribution.** Onboarding, plus signed and notarized macOS releases.

The first end-to-end milestone: an outside agent requests a task through MCP, harness rejects a flawed attempt, retries it, and the UI shows the evidence for the final decision.
