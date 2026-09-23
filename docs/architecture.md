# Luna Agent architecture

## Scope

Luna Agent is a bounded local preview with three deliberately separate layers:

1. an authoritative Go core;
2. replaceable subprocess tool plugins — one process per model-visible tool;
3. a browser UI that observes and controls the core through a small HTTP/SSE contract.

The root application is the current implementation. `spikes/001-plugin-kernel/` is historical verification evidence and is not linked into the root binary.

## Authority boundaries

### Authoritative core

The root Go process owns all application truth:

- startup configuration and OpenAI-compatible model construction;
- run IDs, the one-run-at-a-time admission rule, the default 60-second whole-run context deadline, HTTP request/disconnect cancellation, and Eino's six-iteration limit;
- mapping Eino output and tool callbacks into Luna-owned events;
- event order and SSE framing;
- the active generation of each allowlisted tool, publication of a replacement across the whole tool set, retirement, and owned-process cleanup;
- loopback binding, Host/Origin validation, request limits, and secret-free state;
- the bounded lifecycle records exposed by `/api/state`.

Eino owns the internal model/tool loop. It is an implementation dependency, not Luna's public protocol. Eino messages, callbacks, and structs must not be serialized directly to clients.

The core permits one active run. A second `POST /api/runs` receives `409` rather than sharing a mutable event sink. Run-local context carries the event sink and run ID, preventing events from crossing runs. Client disconnect or an SSE write failure cancels that context. The HTTP handler then joins the runner—continuing to drain its event channel without further writes—before clearing the busy state, so a canceled run cannot overlap its successor. There is no public cancel endpoint.

The current root wraps each admitted model run in an explicit, default 60-second `context.WithTimeout`. That whole-run deadline is below the server's 70-second HTTP write deadline. Component deadlines remain 60 seconds for plugin build and 5 seconds for plugin startup/RPC; an earlier parent deadline can still end any of this work sooner.

### Plugin processes

The model sees exactly two tools, registered by the core from the plugin host's allowlist. Each runs as its own process and each has its own input schema.

`luna_text_transform`:

```json
{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "description": "Text to transform"
    }
  },
  "required": ["text"],
  "additionalProperties": false
}
```

`luna_read_file`:

```json
{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Path of a text file, relative to the configured read root"
    }
  },
  "required": ["path"],
  "additionalProperties": false
}
```

Each implementation is a real HashiCorp `go-plugin` net/rpc child process. Its RPC protocol is private to the core/plugin boundary. A child receives a minimal environment (`PATH`, `HOME`, `TMPDIR`, and `GOCACHE` when present), not the provider's OpenAI variables.

`luna_read_file` splits its boundary in two. `internal/fileread.Resolve` runs on the host and is the only place a model-supplied path is interpreted: it normalizes the path, rejects absolute paths and `..` escapes, resolves symbolic links, and refuses anything that is not a regular file still inside the read root. The plugin is then handed the resolved absolute path plus the cap and never interprets a path itself. A read above the cap (default 256 KiB, `-read-limit`) and content containing a NUL byte are refused with an explicit error rather than truncated or guessed at, and error strings never carry an absolute host path. The read root defaults to the resolved repository root and is set by `-read-root`.

Allowed candidates are compiled from `plugins/<tool>/<candidate>/`, all from root source:

- `v1`: `luna_text_transform` trims surrounding whitespace; `luna_read_file` returns the text of the path the host validated;
- `v2`: `luna_text_transform` trims, uppercases with Go string handling, and prepends `Luna · `; `luna_read_file` normalizes `CRLF` and lone `CR` to `LF`;
- `broken`: starts a program that cannot complete the expected handshake, for either tool.

Each successful tool result returned to the model is JSON containing that tool's result plus the immutable generation, version, and plugin PID that actually served the call. The same metadata is exposed in `tool.finished`. Tool arguments must decode as exactly one JSON object: unknown fields, malformed input, a missing or empty `text` or `path`, and trailing JSON values are rejected with `tool.failed` rather than reaching a plugin.

The Eino `ToolsNode` is configured with `ExecuteSequentially: true`. If a model turn requests multiple tools, Luna invokes them one at a time and preserves `tool.started`/`tool.finished` ordering instead of running plugin calls concurrently.

### Browser UI

The static root `web/` application is a client, not an authority. It:

- submits one chat run and renders assistant deltas;
- renders tool arguments, the serving generation/version/PID, and its own per-tool copy in expandable cards;
- polls secret-free state for the side inspector, which lists one row per plugin record: tool name, status, version, generation and PID;
- requests an allowlisted candidate reload;
- keeps run and reload busy states separate;
- uses DOM APIs and `textContent`, with no remote assets and no hidden-reasoning view.

Polling must not overwrite the composer. A disconnected inspector or failed reload is displayed as an error; the UI must not manufacture success state.

## Request and event flow

```text
Browser
  ├─ POST /api/runs ──> HTTP guard + single-run admission
  │                        └─ 60-second run context
  │                             └─ Eino ChatModelAgent
  │                                  ├─ OpenAI-compatible ChatModel
  │                                  ├─ luna_text_transform wrapper
  │                                  └─ luna_read_file wrapper
  │                                       └─ pinned per-tool plugin generation over net/rpc
  └─ SSE events <──────── Luna-owned run-local event sink

Browser ── POST /api/reload ──> build the candidate for every allowlisted tool
                                └─ start + handshake + metadata validation, per tool
                                     └─ publish one new generation for all tools, or none
```

Model tool choice is automatic. The system instruction requires `luna_text_transform` when the user explicitly requests text transformation or explicitly asks to call it, and `luna_read_file` when the user asks for a file to be read, but no request forces provider-level `tool_choice: required`. Assistant content emitted on a turn that contains tool calls is treated as transient and suppressed, both for streaming and non-streaming model output; only assistant text from a tool-free answer turn becomes `assistant.delta` and contributes to the final answer.

## Public HTTP contract

The listener accepts literal loopback IPs only and defaults to `127.0.0.1:0`. The actual bound address is printed as `LISTEN_URL=...`. Requests must use that exact bound Host. Mutations require the exact same non-null Origin; no CORS policy opens the service to other origins.

Endpoints:

- `GET /healthz` reports whether model configuration exists and every allowlisted tool has an active generation. It does not probe the provider.
- `GET /api/state` reports model/provider labels, the host PID, one plugin record per allowlisted tool (including any retiring generation, each with its candidate, status and in-flight count), single-run status, and bounded lifecycle events. `model_connected` is initially false and becomes true only after a successful run; configuration alone is represented by `model_configured`.
- `POST /api/reload` accepts only `{"candidate":"v1"}`, `v2`, or `broken`, and applies it to every tool or to none.
- `POST /api/runs` accepts `{"message":"..."}` and returns an SSE stream.

Bodies are capped at 32 KiB and unknown JSON fields are rejected. Messages are non-empty and capped at 16,384 bytes. There are no arbitrary-path, arbitrary-command, or user-supplied plugin endpoints.

## SSE contract

The wire framing is:

```text
event: <event-type>
data: <JSON payload>

```

Event types and payloads are application-owned:

- `run.started` — `{"run_id":"..."}`
- `assistant.delta` — `{"text":"..."}`; emitted for provider stream chunks or as one visible assistant message when output is not chunked, but never for a turn containing tool calls
- `tool.started` — `{"run_id":"...","name":"...","arguments":...}`, where `name` is one of the two allowlisted tool names (`luna_text_transform`, `luna_read_file`)
- `tool.finished` — `{"run_id":"...","name":"...","result":"...","generation":N,"version":"...","plugin_pid":N}` for the tool that served the call
- `tool.failed` — run/tool identity and an error, with generation/version/PID when known
- `run.finished` — `{"run_id":"...","answer":"..."}`
- `run.failed` — `{"run_id":"...","error":"..."}`

After a stream is established, `run.finished` or `run.failed` is the single terminal event. The HTTP layer forwards the first terminal event, discards later terminal events, and synthesizes one from the runner result if the runner omitted it. Thus every writable completed stream has exactly one terminal event. Pre-stream validation and admission failures are ordinary JSON HTTP errors instead. The handler flushes every event; client disconnect or write failure stops further writes, cancels the run, and waits for runner exit before releasing single-run admission.

`tool.started.arguments` and `tool.finished.result` can contain user text because they are part of the live run transcript. The core's bounded lifecycle log stores only concise status messages and must not persist prompts, arguments, results, model bodies, authorization headers, secrets, or hidden chain-of-thought.

## Reload semantics

A plugin reload is a generation replacement, not an in-place mutation:

1. compile the selected candidate for every allowlisted tool, each to its own new generated executable;
2. start each child with the restricted environment;
3. complete each plugin handshake and dispense the expected interface;
4. validate protocol version, candidate version, and actual child PID for each tool;
5. reject an expired context before publication;
6. atomically publish one new generation for the whole tool set — if any single tool fails, nothing is published and the candidates that did start are killed;
7. mark each previous generation retiring, let already-pinned calls finish, then terminate it and remove its generated executable.

Consequences:

- `broken` fails before publication and leaves the previous active generation unchanged; because publication is all-or-nothing, no other tool moves either;
- reloading the same version still creates a new generation and PID for every tool;
- new calls use the newly published generation while old in-flight calls finish against the generation they pinned, per tool;
- an RPC timeout or cancellation terminates the owned plugin before the core reports that termination;
- build, startup, and RPC work are bounded.

Reload does **not** reload the Go core, environment variables, model client, HTTP listener, or UI assets. There is no filesystem watcher, automatic rebuild loop, dynamic plugin discovery, arbitrary plugin path, or zero-downtime core restart.

## Verification status

The current root candidate has separate evidence for each major boundary rather than treating a build or fake-model test as end-to-end proof:

- repository-wide Go race tests, vet, root build, Go formatting, Node syntax, and all 13 focused browser-JavaScript tests passed;
- the focused post-disconnect race regression passed 50 repeated race-detector runs;
- a live `deepseek-flash` request at `api.deepseek.com` automatically selected `luna_text_transform` without forced provider `tool_choice`;
- live `v1` and `v2` calls reported their actual generations and plugin PIDs, while a failed `broken` candidate left the active `v2` generation callable;
- real headless Chromium interaction covered live runs, successful and failed reloads, composer draft preservation, zero page errors, and desktop/390 px overflow checks;
- Desktop Preview read the running UI's visible model, provider, host, and plugin metadata;
- final independent review reported no security concerns or logic errors.

The live-provider, headless-Chromium, Desktop Preview and final-review items above were captured for the one-tool kernel. The two-tool change re-ran the Go race, vet, root-build, Go-format, Node syntax and Node test gates; it did not re-run a live provider or a real browser.

No API key appeared in the retained evidence. This is point-in-time validation of the described local architecture, not a guarantee for every OpenAI-compatible provider or browser and not a complete accessibility, load, or production-security assessment.

## Non-goals

This slice intentionally excludes:

- multi-agent orchestration;
- persistent conversations, durable run history, or long-term memory;
- arbitrary shell, filesystem, or network tools;
- browser-provided plugin code or paths;
- runtime UI plugins or a plugin marketplace;
- production authentication, authorization, tenant isolation, or public deployment;
- cross-origin API access;
- hidden chain-of-thought capture or display;
- retries that could duplicate model/tool effects;
- a public cancellation API;
- claims of provider connectivity before a successful run.

The UI phrase “真实模型” describes the configured runtime path. The verification above separately establishes one successful live-provider and real-browser run; it does not turn that label into a general connectivity or production-readiness guarantee.
