# Luna Agent

Luna Agent is a bounded, local, single-user preview of an Agent kernel written in Go. The root application runs an Eino `ChatModelAgent` ReAct loop against an OpenAI-compatible provider, exposes an app-owned SSE event stream, and invokes `luna_text_transform` through a real HashiCorp `go-plugin` subprocess.

This is a kernel slice, not a production agent platform. Runs are single-flight and are not persisted.

## Historical spike

- [001 · Plugin kernel](spikes/001-plugin-kernel/README.md) is the original verified subprocess/net-rpc experiment. It records generation pinning, drain-before-exit, same-version rebuilds, and failed-candidate rollback.

The spike remains historical evidence. The root implementation is authoritative production-shaped code; it does not import the spike or treat spike files as runtime source.

## What the root preview contains

- Eino `ChatModelAgent` named `luna`, automatic tool choice, a six-iteration ceiling, and sequential execution when one model turn contains multiple tool calls.
- OpenAI-compatible configuration read only from the process environment.
- One model-visible tool, `luna_text_transform`, backed by an allowlisted subprocess candidate:
  - `v1`: trim surrounding whitespace.
  - `v2`: trim, uppercase, and prepend `Luna · `.
  - `broken`: intentionally refuses the plugin handshake so rollback can be observed.
- Validated hot reload: build, start, handshake, and metadata checks complete before a new generation is published. In-flight calls remain pinned to their original generation while it drains.
- Loopback-only HTTP service, guarded mutation origins, bounded request bodies, a default 60-second whole-run context deadline, and a compact local UI.
- Public, app-owned SSE events rather than Eino or plugin RPC structs, with exactly one terminal event per writable stream.

## Requirements

- Go compatible with the module declaration in `go.mod` (`go 1.24`).
- Node.js only for the focused browser-JavaScript tests.
- These environment variables at process startup:
  - `OPENAI_BASE_URL`
  - `OPENAI_API_KEY`
  - one or more agreeing model aliases: `OPENAI_MODEL_NAME`, `OPENAI_MODEL`, `OPENAI_MODEL_ID`

If multiple model aliases are set, their non-empty values must agree. Secrets are never accepted from the browser and must not be committed or logged.

## Build

From the repository root:

```sh
go build -o .runtime/luna ./cmd/luna
```

Keep the binary at `.runtime/luna`: the executable location is used to locate the root `plugins/` and `web/` directories.

## Launch

The expected trusted-shell launch is:

```sh
zsh -lc 'source ~/.secrets/llm-dsv4f.env && exec /home/j/probe/luna-agent/.runtime/luna -addr 127.0.0.1:0'
```

**Warning:** this executes `~/.secrets/llm-dsv4f.env` as shell code. Use it only when that file is trusted. The command intentionally shows no secret values. As an alternative, export the required variables in the current trusted shell and run:

```sh
exec /home/j/probe/luna-agent/.runtime/luna -addr 127.0.0.1:0
```

The default ephemeral port avoids collisions. After binding, Luna prints one line in this form:

```text
LISTEN_URL=http://127.0.0.1:<port>
```

Open that exact URL in a browser. The process starts with plugin candidate `v1`; runtime candidate builds require the Go toolchain to remain available on `PATH`.

## Public local API

- `GET /healthz` — configuration/plugin readiness; no provider call is implied.
- `GET /api/state` — bounded runtime state with model name, provider host, host/plugin PIDs, generations, busy state, and lifecycle events; no API key.
- `POST /api/reload` with `{"candidate":"v1|v2|broken"}` — validated candidate replacement or rollback-preserving error.
- `POST /api/runs` with `{"message":"..."}` — `text/event-stream` response using the stable event types documented in [docs/architecture.md](docs/architecture.md).

Mutation requests must come from the exact bound browser origin. There is no CORS support and no public-network mode.

Each admitted run receives a default 60-second context deadline, below the server's 70-second write deadline. Client cancellation or an SSE write failure cancels the run; the handler waits for the runner to exit before clearing the single-run busy state.

## Verification

The current root candidate has passed the following independently run deterministic gates:

```sh
go test -race ./...
go vet ./...
go build -o .runtime/luna ./cmd/luna
node --check web/app.js
node --test web/app.test.cjs
```

The Node test suite reports 3 passing tests. Go formatting passed, and the focused post-disconnect race regression passed 50 repeated race-detector runs. Unit/integration coverage includes configuration alias handling, real subprocess replacement and draining, broken rollback, RPC timeout termination, strict tool schema and trailing-JSON rejection, sequential tool execution, suppression of assistant text from tool-call turns, deterministic fake-model Eino event mapping, whole-run timeout and cancellation cleanup, exact-one SSE terminal semantics, and HTTP guards.

Separate end-to-end validation completed the checks that deterministic tests cannot provide:

- A live request using model `deepseek-flash` at provider host `api.deepseek.com` selected `luna_text_transform` automatically, without forced provider `tool_choice`. The active `v1` plugin returned `moon light` with the serving generation and plugin PID reported by the application. After reload, `v2` used a new generation and plugin PID and returned `Luna · MOON LIGHT`. A `broken` reload failed without replacing `v2`, which remained callable.
- Real headless Chromium interaction exercised the UI through the `v1`, `v2`, and failed `broken` paths, preserved a composer draft during polling/reload, produced no page errors, and showed no horizontal overflow at desktop width or a 390 px viewport.
- Desktop Preview independently opened the running loopback application and read visible model, provider, host, and plugin metadata.
- A final independent review found no security concerns or logic errors.

No API key appeared in the retained verification evidence. These results are point-in-time evidence for the tested provider and headless Chromium path, not a production-readiness claim, a compatibility guarantee for every OpenAI-compatible provider, or a complete accessibility/cross-browser audit.

## Boundaries

The preview deliberately omits multi-agent orchestration, persistent sessions, long-term memory, arbitrary shell/file tools, user-supplied plugin paths, runtime UI plugins, a plugin marketplace, production authentication, and public deployment. See [docs/architecture.md](docs/architecture.md) for ownership and reload semantics.
