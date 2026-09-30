# Harness

Local prototype for contract-driven agent workflows. The current Go proof of concept lives in `app/`.

Run it with `cd app && go run .`.

## Review slices

Each slice should be a separate pull request, reviewed before starting the next one:

1. Return a result and failure reason from a deterministic contract check. (This PR)
2. Define and validate a versioned graph of acceptance rules and gates (not a prescribed coding order) that users can draft and publish.
3. Record runs, attempts, and the evidence used for each decision.
4. Evaluate subjective rules with a user-selected reviewer (Jev or a local/remote model).
5. Run a worker in an isolated workspace and retry failed attempts within limits.
6. Expose task requests and run status to external agents through MCP.
7. Add a minimal desktop graph editor, onboarding, and run-evidence view.
8. Demonstrate the full flow: MCP request, rejected attempt, retry, and visible evidence.
