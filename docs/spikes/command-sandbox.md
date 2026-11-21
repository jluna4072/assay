# Spike: sandboxing approved commands on macOS

Phase 2 lets contracts require the result of user-approved commands (for example, `go test ./...`) as evidence. This spike checks whether assay can run those commands so they can't reach the network, write outside where they're allowed, or read credentials, using macOS's built-in Seatbelt sandbox through `/usr/bin/sandbox-exec`.

**Verdict: viable.** Every restriction tested held, including for child processes. `sandbox-exec` is marked deprecated, so assay must detect it and refuse to run command evidence unsandboxed unless the user explicitly opts out.

Tested on macOS 27.2 (build 26B123), Apple silicon, November 2026.

## What was tested

Profile, with `PROJECT` and `SECRETS` passed as parameters (`sandbox-exec -D PROJECT=… -D SECRETS=… -f profile.sb <command>`):

```scheme
(version 1)
(allow default)
(deny network*)
(deny file-write*)
(allow file-write*
  (subpath (param "PROJECT"))
  (literal "/dev/null")
  (literal "/dev/dtracehelper"))
(deny file-read* (subpath (param "SECRETS")))
```

| Attempt | Result |
|---|---|
| Write a file inside the project | Allowed |
| Write outside the project, to `$HOME`, to `/tmp` | Blocked (`Operation not permitted`) |
| A grandchild process (`sh -c "sh -c 'touch …'"`) writes outside | Blocked; the sandbox is inherited |
| Write through a symlink in the project that points outside | Blocked |
| Read a file under `SECRETS` | Blocked |
| Read files in the project | Allowed |
| HTTPS request, DNS lookup | Blocked (`Could not resolve host`) |
| Connect to a server on `127.0.0.1` | Blocked |
| Same, with `(allow network* (remote ip "localhost:*"))` added | Allowed, while internet access stays blocked |
| Start a second, looser sandbox from inside the sandbox | Refused (`sandbox_apply: Operation not permitted`, exit 71) |
| `go test ./...` with default environment | Fails: Go's temp dir is outside the allowed writes |
| `go test ./...` with `TMPDIR` and `GOCACHE` inside the allowed area | Passes |
| `git status`, `git commit` inside the project | Pass |
| `go get` of a module not in the module cache | Blocked (no DNS) |
| `go get` of a module already in `~/go/pkg/mod` | Succeeds from the cache; reads aren't restricted by default |

`sandbox-exec` printed nothing extra on stderr and added about 10 ms per command.

## What this means for Phase 2

1. **Generate a profile per command** from assay's own template, never from contract text. Parameters (`-D`) carry paths, so paths are never spliced into profile source.
2. **Writes go only to a per-attempt scratch area by default**, `.assay/tmp/<run>/<attempt>/`, with `TMPDIR` pointed there. Not the whole project: if commands could write anywhere in the project, a command could change files after assay captured them as evidence. A command that has to write in the project (a code generator, say) declares it in `.assay/config.yaml` (`writes: project`), and assay captures file and diff evidence before running it.
3. **Tool caches need a home.** Approved commands declare cache directories (`GOCACHE`, npm's cache, and so on); assay creates them under `.assay/cache/<name>/`, allows writes there, and sets the variables. Caches persist across attempts so runs stay fast.
4. **No network by default.** Commands must work offline; already-downloaded dependencies are readable. A command can declare `network: localhost` (for tests that start a local server) or, explicitly, `network: on`.
5. **Deny reads of credential locations** by default: `~/.ssh`, `~/.aws`, `~/.config/gh`, `~/.netrc`, `~/.docker`, `~/Library/Keychains`, and assay's own `.assay/assay.db`. Allowing reads only from an allowlist isn't practical, because toolchains read many system paths.
6. **The environment is cleared** to an allowlist (`PATH`, `HOME`, `LANG`, plus what the command's config names), as the plan already says. The sandbox doesn't stop a command from reading environment variables it's given.

## What this means for Phase 4

A process inside a Seatbelt sandbox can't apply another sandbox. Codex sandboxes itself with the same mechanism, so assay can't wrap the worker in its own sandbox and still let Codex apply its sandbox. The worker runs under Codex's sandbox (`--sandbox workspace-write`, network off); assay's sandbox applies to the commands assay itself runs for evidence. The Phase 4 docs need to say this plainly.

## Risks and fallbacks

- **Deprecation.** The man page has called `sandbox-exec` deprecated for years, and it still ships and works; agent tools such as Codex CLI build their macOS sandboxes on it. Apple could still remove it. assay should check that it exists and works (run `/usr/bin/true` under a trivial profile) before running command evidence, and `assay doctor` should report the result.
- **No sandbox available** (removed, or another OS): refuse to collect command evidence, with an error that says why. `sandbox: off` in `.assay/config.yaml` allows it, and every attempt run that way is recorded as unsandboxed in its evidence.
- **Linux** isn't covered by this spike. Landlock (filesystem) plus a network namespace, or bubblewrap, would be the equivalent.
- **Not tested:** limits on CPU, memory, or process count (the sandbox doesn't provide them; assay's timeout and process-group kill are the backstop), and long-running or interactive commands.
