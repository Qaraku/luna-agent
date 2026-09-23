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
- Do not stage, commit, push, reset, or delete user work unless the task explicitly authorizes it.

## Commit boundaries

When commits are explicitly requested, keep these histories separate:

1. **spike** — changes under `spikes/` only;
2. **core** — Go kernel, subprocess plugins, module files, and core documentation;
3. **web** — `web/` HTML/CSS/JavaScript and focused browser-JavaScript tests.

Do not combine spike, core, and web changes in one commit. Repository-wide documentation may use its own documentation commit when that makes the boundary clearer.
