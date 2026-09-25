# AGENTS.md

Instructions for automated contributors working in this repository.

## Scope and ownership

- Treat the root application as the authoritative implementation: `cmd/`, `internal/`, `plugins/`, `web/`, `go.mod`, and `go.sum`.
- Treat `spikes/001-plugin-kernel/` as immutable historical evidence unless a task explicitly targets that spike.
- Do not modify a spike and present it as production work. Port an idea into root-owned packages and test it there; never import across the spike's `internal/` boundary.
- Keep core, subprocess plugin, HTTP/SSE, and UI contracts explicit. Do not expose Eino structs or HashiCorp RPC structs as the public browser protocol.

## Bounded commands

Run commands from the repository root and give every potentially blocking operation a deadline. Suitable local gates are:

```sh
timeout 180s go test -race ./...
timeout 60s go vet ./...
timeout 120s go build -o .runtime/luna ./cmd/luna
timeout 30s node --check web/app.js
timeout 30s node --test web/app.test.cjs
git diff --check
```

- Use ephemeral loopback addresses (`127.0.0.1:0`) for runtime checks.
- If a server is needed for a bounded check, capture its PID, apply a timeout, terminate it, wait for exit, and verify that no owned server/plugin process remains.
- Do not leave persistent servers running.
- Do not operate a server or generated binary under `spikes/` unless the task explicitly requires spike work.

## Secrets and evidence

- Never read, print, copy, commit, or persist credential values, and never include them in evidence, logs, or lifecycle output.
- Never log API keys, authorization headers, provider request/response bodies, prompts, tool arguments/results, or hidden reasoning in lifecycle evidence.
- Application startup errors may identify missing environment-variable names, but must not include their values.
- Only source a trusted secrets file when an explicitly authorized live-provider check requires it. The normal implementation and deterministic test path must use fake models or process-environment stubs.
- Keep local logs and evidence in ignored paths such as `.evidence/`; do not overwrite source documentation with captured output.

## Change discipline

- Use test-driven vertical slices and retain honest RED/GREEN command evidence in the implementation report.
- Keep the tool set and candidate selection allowlisted. The core registers every model-visible tool itself, and a candidate name (`v1`, `v2`, `broken`) may only come from that allowlist. No browser-supplied path, tool name, or candidate name may become a build path or an executed command, and nothing beyond the allowlist may be executed as a shell command.
- `luna_read_file` is the only filesystem capability, and it is bounded:
  - it reads only inside the configured read root;
  - the host normalizes the requested path, rejects absolute paths and `..` escapes, then resolves symbolic links and rejects any path that is not still inside the read root, so a symlink cannot be used to escape;
  - a single read above the size cap (default 256 KiB) is refused with an explicit error instead of being truncated, and binary content (any NUL byte in the bytes actually read) is refused;
  - it is read-only text: no writes, no execution, no network;
  - the plugin receives an already-validated absolute path plus the cap, and never interprets a model- or browser-supplied path itself.
- Preserve bounded timeouts, one-run-at-a-time isolation, exact terminal SSE semantics, and secret-free state responses.
- A tool refusal is not a run failure. A refusal about the call itself — a rejected path, the single-read size cap, binary content, a malformed argument — goes back to the model as the call's result under the `the tool refused this call: ` prefix, while `tool.failed` still reports it to the UI and the transcript keeps the failed call. Only the plugin host's infrastructure sentinels (`ErrUnknownTool`, `ErrNoActivePlugin`, `ErrRPCTimeout`, `ErrRPCCanceled`, `ErrPluginGone`) end the run: classify by sentinel, never by message text.
- A change that adds or removes an API surface, a directory, or a tool updates `README.md` and `docs/architecture.md` in the same change, and this file with the user's approval. Re-read the sentences that promise what the code does and correct every count. A stale sentence in a public README is a defect, not a nit.
- Do not stage, commit, push, reset, or delete user work unless the task explicitly authorizes it.

## Surfaces that must stay honest

Each capability that holds state or serves code has one property that the design exists to protect. Losing it is not a regression to fix later; it is the feature disappearing.

- **Memory** is host-native: core state a `broken` candidate must not be able to replace, so it has no generation and cannot be reloaded. The model writes and never reads; the stored caps and the injection caps are separate limits; and a memory file that cannot be read fails the run instead of pretending nothing was remembered. A retraction is an appended `retract` record folded out of the effective set on read, matched by the fact's text and timestamp together; the byte cap counts every line, so retracting cannot grow the file, and a rewrite compacts a retraction away with its fact. The model side gains nothing from it: no read, list, edit or retract path, and `/api/state` still reports no memory.
- **Sessions** are append-only JSONL. An id comes from `crypto/rand`, is validated before any filesystem use, and is created with `O_EXCL`; a file is opened `O_RDWR|O_APPEND` (never `O_WRONLY`, which breaks torn-line detection) and each append is one whole line; an unknown id is a not-found, never a new session.
- **UI plugins** are served only from `plugins/ui/<name>/`, with containment checked after normalization and again after resolving symlinks through `internal/fileread`'s validator — a second implementation of that check is exactly where the strength drops. The host removes the container even when `unmount` throws, and enable state is not persisted.

## Commit boundaries

When commits are explicitly requested, keep these histories separate:

1. **spike** — changes under `spikes/` only;
2. **core** — Go kernel, subprocess plugins, module files, and core documentation;
3. **web** — `web/` HTML/CSS/JavaScript and focused browser-JavaScript tests.

Do not combine spike, core, and web changes in one commit. Repository-wide documentation may use its own documentation commit when that makes the boundary clearer.
