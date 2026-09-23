# Luna Agent architecture

## Scope

Luna Agent is a bounded local preview with three deliberately separate layers:

1. an authoritative Go core;
2. replaceable subprocess tool plugins — one process per plugin-backed tool;
3. a browser UI that observes and controls the core through a small HTTP/SSE contract.

The root application is the current implementation. `spikes/001-plugin-kernel/` is historical verification evidence and is not linked into the root binary.

## Authority boundaries

### Authoritative core

The root Go process owns all application truth:

- startup configuration and OpenAI-compatible model construction;
- run IDs, the one-run-at-a-time admission rule, the default 60-second whole-run context deadline, HTTP request/disconnect cancellation, and Eino's six-iteration limit;
- mapping Eino output and tool callbacks into Luna-owned events;
- event order and SSE framing;
- the active generation of each allowlisted plugin tool, publication of a replacement across the whole plugin tool set, retirement, and owned-process cleanup;
- loopback binding, Host/Origin validation, request limits, and secret-free state;
- the bounded lifecycle records exposed by `/api/state`;
- the durable session records: creating a session, appending one record per line to its file, and reading a session's history back from disk for a later run's model input;
- the durable memory facts: appending one fact per line through the host-native `luna_remember` tool, bounding the file with two caps, and reading the stored facts back on every run to inject them into the system prompt under their own caps.

Eino owns the internal model/tool loop. It is an implementation dependency, not Luna's public protocol. Eino messages, callbacks, and structs must not be serialized directly to clients.

The core permits one active run. A second `POST /api/runs` receives `409` rather than sharing a mutable event sink. Pre-admission failures keep their own status instead: a malformed body or message is `400`, and an unknown session id is `404`, both decided before the busy check. Run-local context carries the event sink and run ID, preventing events from crossing runs. Client disconnect or an SSE write failure cancels that context. The HTTP handler then joins the runner—continuing to drain its event channel without further writes—before clearing the busy state, so a canceled run cannot overlap its successor. There is no public cancel endpoint.

The current root wraps each admitted model run in an explicit, default 60-second `context.WithTimeout`. That whole-run deadline is below the server's 70-second HTTP write deadline. Component deadlines remain 60 seconds for plugin build and 5 seconds for plugin startup/RPC; an earlier parent deadline can still end any of this work sooner.

### Session storage

`internal/store` owns the session files and is the only package that touches them. `internal/httpapi` creates a session and reads sessions back for `GET /api/sessions` and `GET /api/sessions/{id}`; `internal/agent` reads a session's history and appends a run's records through the store's own methods. One session is one append-only JSONL file, `<id>.jsonl`, in the session directory (`-sessions-dir`, default `<root>/.runtime/sessions/`, directory `0700`, files `0600`). An id is 12 random bytes in lowercase hex and is validated against a lowercase alphanumeric charset and a length bound before it can name a file, so no separator, no dot and no empty string reaches the filesystem; an id that fails that check is `400`, and an id with no file behind it is `404`.

The format is append-only and frozen. Every record is one `Write` call terminated by a newline, so a crash can lose only the unterminated fragment at the tail and never a completed record. No record is ever rewritten; there is no database and no migration. Every write takes one store mutex, so concurrent runs cannot interleave halves of a line inside one session, and a session directory belongs to a single process. The record types are `session` (the first line: id, `created_at`, title), `message` (run id, role `user` or `assistant`, text, timestamp), `tool_call` (run id, tool name, the model's raw argument text, result or error, timestamp), and `run` (run id, start, end, status `ok` / `error` / `cancelled`). A `run` record is written once, after the run ends, so a crash mid-run leaves the run unrecorded rather than half-recorded; `interrupted` is reserved for exactly that case and is not written by this slice. Plugin identity — generation, version, process id — has no field in a record, so it cannot travel back into a model context through the replay path.

A torn final line is the one tolerated defect. An unterminated trailing fragment is dropped on read, reported as `truncated: true` to the client rather than failing the session, and the store truncates that fragment before an append so a new record is never merged into it. A malformed record anywhere else is a loud `ErrCorrupt` error, never a silent gap, and surfaces as `500`.

### Memory

`internal/memory` owns the fact file and is the only package that touches it. Memory is core state, not an extension: the core opens the store, reads it on every run, and registers the host-native `luna_remember` tool that writes it. The model can append a fact and has no tool that reads, lists, or deletes one, and no HTTP endpoint exposes the file.

One memory is one append-only JSONL file, `-memory-file` with a default of `<root>/.runtime/memory.jsonl`, directory `0700` and file `0600`. The record shape is frozen by the S3a spec: `type` (always `fact`), `text`, `at`, `source_session`. As with a session record, the shape has no field for plugin identity and no field for a credential, so neither can travel through memory into a model context. `source_session` records which session wrote a fact; it is storage bookkeeping and is not shown to the model. A write also lands in the session transcript as the `tool_call` record it was, because every tool call does, so fact text can exist in both files: the memory file is the only one read back, and the history replay still carries message text only.

The append-only guarantee is the same one the session store makes, restated for this file. Each fact is encoded as one newline-terminated line and written with a single `Write` call to a file opened read-write and appended to, so a crash can lose only the unterminated fragment at the tail and never a completed record, and no stored fact is ever rewritten. A missing file reads as an empty memory, because the first run of a fresh checkout has no facts. Both the read side and the write side take one store mutex, so concurrent runs cannot interleave halves of a line and a read-modify-write cannot lose a fact.

Two caps bound what is stored: `MaxFacts = 200` facts and `MaxBytes = 32 KiB` of encoded lines, with `MaxFactChars = 500` characters on one fact's text. Above either cap the oldest facts are dropped, and that drop is the one case that rewrites the file — atomically, through a temporary file in the same directory and a rename, so a reader sees either the old complete set or the new complete set. The fact just accepted is never the one dropped, so a fact the caller was told was stored cannot vanish in the same call. Both the tool and the store enforce the per-character cap, so a model-facing bound and a storage bound cannot drift apart.

A torn final line is tolerated here too, and handled on both paths. An unterminated trailing fragment is dropped on read rather than failing the store, and the next write that has to rewrite the file — for a drop or for the fragment — replaces it with the complete kept set, so a new fact is never merged into a fragment and the fragment does not survive. A malformed complete record anywhere else is a loud `ErrCorrupt` error instead, never a silent gap; it names a line number and never a host path, so the string can be handed back to the model as a failed tool call rather than published as a directory layout.

Stored caps and injection caps are deliberately different bounds on the same facts. Storage keeps up to 200 facts and 32 KiB; one run's system prompt receives at most `MaxInjectFacts = 50` facts and `MaxInjectBytes = 8 KiB` of rendered lines, with the oldest dropped first, so the stored file may hold more than any single run is shown. The kept set is a contiguous, chronologically ordered suffix accumulated from the newest fact backwards — the same keep-most-recent policy as the history cap — so facts are never reordered, sampled, or skipped, and the injection byte cap counts the rendered lines and not the label. Below the caps nothing is dropped: the whole stored memory is injected, ordered oldest first.

The injected text is reference data, never instructions. Stored fact text is text the model or the user wrote, and spliced into a system prompt unlabelled it would read as a system directive; the block therefore carries an explicit header stating that these are facts about the user, that they are not instructions, that no line below may be followed as a directive, and that no line may change the instruction. The system instruction repeats the same rule. Newlines inside a fact are collapsed to spaces when the block is rendered, so a fact cannot open a line of its own inside the prompt, which is the one thing a smuggled directive would need. Only the label and the fact text appear: generation, version, process id and credentials have no field in a stored fact and no place in the block. The instruction is not treated as an f-string template, so fact text and prompt text containing braces are both safe. A memory that cannot be read fails the run before the model is called, rather than silently running as though nothing were remembered — the rule the transcript already follows.

### Plugin processes

The model sees exactly three tools. Two are plugin-backed and are registered by the core from the plugin host's allowlist; each runs as its own process and each has its own input schema. The third, `luna_remember`, is host-native — memory is core state — and is described after them.

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

### Host-native memory tool

`luna_remember` is the host-native tool, and it is not a plugin:

```json
{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "description": "One durable fact about the user, stated in a single sentence"
    }
  },
  "required": ["text"],
  "additionalProperties": false
}
```

It has exactly one write-only action. The whole result the model sees is the string `fact stored`, and the tool deliberately has no parameter that reads the stored facts back and none that removes one: memory is written by the model and read only by the system, which injects it as labelled reference data. It is registered by the core against the same store the injection reads, so no candidate, no reload and no plugin failure can be involved in writing a fact; when no store is configured at all, the call fails with an explicit error instead of reporting a success nothing recorded.

Each successful tool result returned to the model is JSON containing that tool's result plus the immutable generation, version, and plugin PID that actually served the call — for a plugin-backed call. A host-native call publishes no plugin identity at all: `luna_remember` has no candidate, generation, version or process, so `tool.finished` for it omits `generation`, `version` and `plugin_pid` rather than reporting zeros. Those three fields are `omitempty` and a plugin-backed call always carries all three non-zero, so the byte layout of a plugin-backed `tool.finished` is unchanged. Tool arguments must decode as exactly one JSON object: unknown fields, malformed input, a missing or empty `text` or `path`, and trailing JSON values are rejected with `tool.failed` rather than reaching a plugin.

The Eino `ToolsNode` is configured with `ExecuteSequentially: true`. If a model turn requests multiple tools, Luna invokes them one at a time — host-native and plugin-backed alike — and preserves `tool.started`/`tool.finished` ordering instead of running tool calls concurrently.

### Browser UI

The static root `web/` application is a client, not an authority. It:

- submits one chat run and renders assistant deltas;
- renders tool arguments, the serving generation/version/PID, and its own per-tool copy in expandable cards;
- polls secret-free state for the side inspector, which lists one row per plugin record: tool name, status, version, generation and PID;
- requests an allowlisted candidate reload;
- keeps run and reload busy states separate;
- uses DOM APIs and `textContent`, with no remote assets and no hidden-reasoning view.

The session API has no front end: `web/` neither lists sessions nor resumes one, and the session endpoints were added for a later UI slice.

Memory has no front end either, and none is implied. `web/` never shows a stored fact, never offers a way to add or delete one, and reads only the plugin records in `/api/state`; a memory write is visible in the transcript as the tool call it was, with no plugin identity to display, and it is never listed as memory.

Polling must not overwrite the composer. A disconnected inspector or failed reload is displayed as an error; the UI must not manufacture success state.

## Request and event flow

```text
Browser
  ├─ POST /api/runs ──> HTTP guard + session resolution + single-run admission
  │                        └─ 60-second run context
  │                             └─ Eino ChatModelAgent
  │                                  ├─ system prompt <── instruction + internal/memory facts (labelled reference data)
  │                                  ├─ session history <── internal/store (append-only JSONL)
  │                                  ├─ OpenAI-compatible ChatModel
  │                                  ├─ luna_text_transform wrapper ──> pinned plugin generation over net/rpc
  │                                  ├─ luna_read_file wrapper ──> pinned plugin generation over net/rpc
  │                                  └─ luna_remember (host-native) ──> internal/memory (append-only JSONL)
  └─ SSE events <──────── Luna-owned run-local event sink

Browser ── GET /api/sessions, GET /api/sessions/{id} ──> internal/store (read-only)

Browser ── POST /api/reload ──> build the candidate for every allowlisted plugin tool
                                └─ start + handshake + metadata validation, per tool
                                     └─ publish one new generation for all of them, or none
```

Model tool choice is automatic. The system instruction requires `luna_text_transform` when the user explicitly requests text transformation or explicitly asks to call it, and `luna_read_file` when the user asks for a file to be read, and it directs `luna_remember` when the user asks for a durable fact to be remembered; no request forces provider-level `tool_choice: required`. Assistant content emitted on a turn that contains tool calls is treated as transient and suppressed, both for streaming and non-streaming model output; only assistant text from a tool-free answer turn becomes `assistant.delta` and contributes to the final answer.

Each turn's model input is assembled from disk rather than from process state: the system instruction, then the session's earlier messages in file order, then this turn's user message. Memory is appended when there are stored facts, inside that system message rather than after the messages: the injected block is the instruction plus the labelled block described in the Memory section, so the system role keeps its position and fact text cannot arrive as a later message. The earlier messages are capped by two constants in `internal/agent` — `MaxHistoryMessages = 40` messages and `MaxHistoryBytes = 64 KiB` of message text — and the policy is deterministic: keep the most recent messages, drop the oldest first. The kept set is a contiguous suffix of the persisted history, accumulated from the newest message backwards until either cap would be exceeded, so a message is never reordered, sampled, or skipped over, and everything older than the point where the scan stopped is dropped as well. A message too large for the byte cap therefore ends the history rather than being truncated. The byte cap sits above the 16,384-byte HTTP message cap, so this turn's own message always fits. A run's own records are excluded from its own input, and only message text enters it: no tool result and no plugin identity can reach the model through the persisted history. Summarization and retrieval are absent from this slice: the history is replayed and memory is injected, and neither is searched, filtered or summarized.

Persistence is part of a run's outcome: the user message is appended before the model runs, the assistant answer and one `run` record after it, and a transcript that cannot be written fails the run rather than reporting a success no restart could reproduce.

## Public HTTP contract

The listener accepts literal loopback IPs only and defaults to `127.0.0.1:0`. The actual bound address is printed as `LISTEN_URL=...`. Requests must use that exact bound Host. Mutations require the exact same non-null Origin; no CORS policy opens the service to other origins.

Endpoints:

- `GET /healthz` reports whether model configuration exists and every allowlisted tool has an active generation. It does not probe the provider.
- `GET /api/state` reports model/provider labels, the host PID, one plugin record per allowlisted tool (including any retiring generation, each with its candidate, status and in-flight count), single-run status, the ids of the current run and session (empty when idle), and bounded lifecycle events. `model_connected` is initially false and becomes true only after a successful run; configuration alone is represented by `model_configured`.
- `POST /api/reload` accepts only `{"candidate":"v1"}`, `v2`, or `broken`, and applies it to every tool or to none.
- `GET /api/sessions` lists session summaries, newest `updated_at` first, as `{"sessions":[{"id":...,"title":...,"updated_at":...,"run_count":...}]}`. `updated_at` and `run_count` are derived from the session's records rather than stored in a separate index.
- `GET /api/sessions/{id}` returns the replay object `{"id":...,"title":...,"created_at":...,"updated_at":...,"run_count":...,"truncated":...,"records":[...]}`. Each element of `records` is the stored JSONL line itself, in file order, so a client replays exactly what is on disk. A malformed id is `400`, an id with no session file is `404`, and a session file whose records cannot be decoded is `500`.
- `POST /api/runs` accepts `{"message":"...","session_id":"..."}` and returns an SSE stream. `session_id` is optional and must name an existing session: an unknown id is `404` and a malformed one `400`, both checked before single-run admission, so neither costs the caller the run slot and neither creates a session. When it is omitted, the core creates a session titled with the message (whitespace collapsed, cut to 80 runes) and reports its id on `run.started`.

Bodies are capped at 32 KiB and unknown JSON fields are rejected. Messages are non-empty and capped at 16,384 bytes. There are no arbitrary-path, arbitrary-command, or user-supplied plugin endpoints, and none for memory: no endpoint reads, lists, or deletes stored facts.

## SSE contract

The wire framing is:

```text
event: <event-type>
data: <JSON payload>

```

Event types and payloads are application-owned:

- `run.started` — `{"run_id":"...","session_id":"..."}`; the session the run was admitted into, created by this request when it supplied none. The terminal-event rule below is unchanged.
- `assistant.delta` — `{"text":"..."}`; emitted for provider stream chunks or as one visible assistant message when output is not chunked, but never for a turn containing tool calls
- `tool.started` — `{"run_id":"...","name":"...","arguments":...}`, where `name` is one of the three model-visible tool names: the plugin-backed `luna_text_transform` and `luna_read_file`, or the host-native `luna_remember`
- `tool.finished` — `{"run_id":"...","name":"...","result":"...","generation":N,"version":"...","plugin_pid":N}` for a plugin-backed tool that served the call; for the host-native `luna_remember` the three identity fields are absent from the JSON rather than sent as zero, because no plugin served it
- `tool.failed` — run/tool identity and an error, with generation/version/PID when a plugin served the attempt and omitted when it did not
- `run.finished` — `{"run_id":"...","answer":"..."}`
- `run.failed` — `{"run_id":"...","error":"..."}`

After a stream is established, `run.finished` or `run.failed` is the single terminal event. The HTTP layer forwards the first terminal event, discards later terminal events, and synthesizes one from the runner result if the runner omitted it. Thus every writable completed stream has exactly one terminal event. Pre-stream validation and admission failures are ordinary JSON HTTP errors instead. The handler flushes every event; client disconnect or write failure stops further writes, cancels the run, and waits for runner exit before releasing single-run admission.

`tool.started.arguments` and `tool.finished.result` can contain user text because they are part of the live run transcript. The core's bounded lifecycle log stores only concise status messages and must not persist prompts, arguments, results, model bodies, authorization headers, secrets, or hidden chain-of-thought.

A session file is a separate record with a different rule: it deliberately stores the run's message text, the model's raw tool arguments, and the tool result, because replay needs them. It stores no model request or response body beyond that, no authorization header, no secret, and no hidden reasoning, and no plugin identity (see the Session storage section above).

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

Reload does **not** reload the Go core, environment variables, model client, HTTP listener, or UI assets, and it cannot reach memory, which is core state and has no generation to replace. There is no filesystem watcher, automatic rebuild loop, dynamic plugin discovery, arbitrary plugin path, or zero-downtime core restart.

## Verification status

The current root candidate has separate evidence for each major boundary rather than treating a build or fake-model test as end-to-end proof:

- repository-wide Go race tests, vet, root build, Go formatting, Node syntax, and all 22 focused browser-JavaScript tests passed;
- the focused post-disconnect race regression passed 50 repeated race-detector runs;
- a live `deepseek-flash` request at `api.deepseek.com` automatically selected `luna_text_transform` without forced provider `tool_choice`;
- live `v1` and `v2` calls reported their actual generations and plugin PIDs, while a failed `broken` candidate left the active `v2` generation callable;
- real headless Chromium interaction covered live runs, successful and failed reloads, composer draft preservation, zero page errors, and desktop/390 px overflow checks;
- Desktop Preview read the running UI's visible model, provider, host, and plugin metadata;
- final independent review reported no security concerns or logic errors.

The live-provider, headless-Chromium, Desktop Preview and final-review items above were captured for the one-tool kernel. The two-tool change re-ran the Go race, vet, root-build, Go-format, Node syntax and Node test gates; it did not re-run a live provider or a real browser.

The session change re-ran those same gates and nothing more. No server and no model provider were run for it, so the session endpoints, the append-only store under a real crash, the history replay, and the terminal-event contract are not claimed as end-to-end verified, and no browser was exercised against them.

The memory change is covered by those same deterministic gates, which pass on this tree. No server and no model provider were run, so memory is not claimed as end-to-end verified either: the append-only fact file under a real crash, the injected block as a live provider receives it, and the memory tool's events in a real run were not exercised outside deterministic tests, and no browser was used. Those tests cover the store's round-trip, both caps, the torn trailing line and its repair, the whole-line guarantee under concurrent writers, the strict write-only tool schema, the injected block's label and ordering, the count and byte caps on injection, the newline collapse, the read failure that fails the run, and the absence of plugin identity on a host-native call. None of that is a substitute for the missing live run.

No API key appeared in the retained evidence. This is point-in-time validation of the described local architecture, not a guarantee for every OpenAI-compatible provider or browser and not a complete accessibility, load, or production-security assessment.

## Non-goals

This slice intentionally excludes:

- multi-agent orchestration;
- long-term memory in the sense of retrieval, embedding, ranking, or automatic extraction: memory exists only in the bounded form described above, an append-only fact file the model appends to through `luna_remember` and the system prompt injects under its own caps;
- summarization or retrieval over a session's history, and any search over stored facts;
- arbitrary shell, filesystem, or network tools;
- browser-provided plugin code or paths;
- runtime UI plugins or a plugin marketplace;
- a browser front end for sessions (the session API has no UI in this slice), and any memory UI: nothing lists stored facts, and nothing adds, edits, or deletes one from the browser;
- production authentication, authorization, tenant isolation, or public deployment (memory is single-user state with no per-user isolation, which is the same boundary);
- cross-origin API access;
- hidden chain-of-thought capture or display;
- retries that could duplicate model/tool effects;
- a public cancellation API;
- claims of provider connectivity before a successful run.

The UI phrase “真实模型” describes the configured runtime path. The verification above separately establishes one successful live-provider and real-browser run; it does not turn that label into a general connectivity or production-readiness guarantee.
