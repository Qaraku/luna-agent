# Luna Agent root slice — implementation report

## Result

The root candidate contains a bounded Eino-based Agent kernel, a real replaceable subprocess tool, a guarded loopback HTTP/SSE surface, and a local chat/trajectory UI. The historical `spikes/001-plugin-kernel/` tree was preserved and is not part of the root runtime dependency graph.

Independent deterministic, live-provider, real-browser, and final-review verification completed successfully. The live evidence contained no API key.

## Exact root candidate file inventory

### CORE / DOC

Created by the implementation worker, as recorded in the handoff summary:

- `go.mod`
- `go.sum`
- `cmd/luna/main.go`
- `cmd/luna/main_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/pluginprotocol/protocol.go`
- `internal/pluginhost/host.go`
- `internal/pluginhost/host_test.go`
- `internal/agent/agent.go`
- `internal/agent/agent_test.go`
- `internal/httpapi/http.go`
- `internal/httpapi/http_test.go`
- `plugins/v1/main.go`
- `plugins/v2/main.go`
- `plugins/broken/main.go`

Documentation completion:

- updated `README.md`
- updated `.gitignore`
- created `AGENTS.md`
- created `docs/architecture.md`
- created `IMPLEMENTATION_REPORT.md`

### WEB

Created by the implementation worker:

- `web/index.html`
- `web/style.css`
- `web/app.js`
- `web/app.test.cjs`

No file under `spikes/` was created or modified by this root slice completion.

## Dependency versions

The checked-in module declares `go 1.24`. Relevant resolved dependencies in `go.mod` are:

| Dependency | Version | Role |
|---|---:|---|
| `github.com/cloudwego/eino` | `v0.9.19` | `ChatModelAgent`, runner, model/tool interfaces |
| `github.com/cloudwego/eino-ext/components/model/openai` | `v0.1.13` | OpenAI-compatible chat model |
| `github.com/cloudwego/eino-ext/libs/acl/openai` | `v0.1.17` | resolved OpenAI compatibility layer |
| `github.com/eino-contrib/jsonschema` | `v1.0.3` | strict tool input schema |
| `github.com/hashicorp/go-plugin` | `v1.8.0` | subprocess handshake and net/rpc transport |
| `github.com/hashicorp/go-hclog` | `v1.6.3` | plugin client logger interface |

`go.mod` and `go.sum` are the authoritative full dependency lock; this table identifies the implementation-defining versions.

## Implemented behavior

- Configuration requires `OPENAI_BASE_URL`, `OPENAI_API_KEY`, and an agreeing value from `OPENAI_MODEL_NAME`, `OPENAI_MODEL`, or `OPENAI_MODEL_ID`. Errors identify variable names without exposing values.
- Eino runs Agent `luna` with automatic tool choice, a concise truthful-demo instruction, streaming enabled, at most six iterations, and `ExecuteSequentially: true` for multiple tool calls in one model turn.
- `luna_text_transform` has a closed JSON object schema with required string field `text`; decoding rejects unknown fields, malformed or trailing JSON, and missing or empty text before plugin invocation.
- Tool events use a run-local sink. The tool result and public event carry the actual generation, version, and plugin PID that performed the invocation.
- Plugin candidates are allowlisted to `v1`, `v2`, and `broken`. Candidate build/start/handshake/metadata validation precedes publication. Calls pin generations; old generations drain; broken candidates preserve the active generation; same-version reloads create a new process; owned processes are cleaned up.
- The HTTP listener requires a literal loopback address. Exact Host and mutation Origin checks, strict/capped JSON, one active run, and secret-free state are enforced. Each admitted run receives a default 60-second whole-run context deadline, below the 70-second server write deadline.
- Client cancellation or SSE write failure cancels the run and stops further stream writes. The handler waits for the runner to exit before clearing the busy state.
- SSE uses Luna-owned event names and guarantees exactly one terminal `run.finished` or `run.failed` event on each writable completed stream by de-duplicating runner terminals and synthesizing one when absent.
- Assistant text from any model turn containing tool calls is suppressed for streaming and non-streaming output; only tool-free answer turns produce visible assistant deltas and final-answer text.
- The UI shows assistant output, tool trajectory metadata, runtime state, and real candidate reload controls without rendering hidden reasoning.

## Honest RED / GREEN history

The implementation worker's handoff summary records RED evidence before each vertical slice. The observed failures were:

- configuration slice: `config.Load` undefined;
- plugin-host slice: required `pluginhost` APIs undefined;
- Agent slice: required `agent` APIs undefined;
- HTTP slice: required HTTP APIs undefined;
- web slice: frontend files absent;
- command slice: `rootFromExecutable` undefined.

The handoff did not preserve full RED terminal transcripts, so this report does not invent exact compiler output or claim stronger evidence.

The same handoff records these GREEN commands and results before final root integration:

- `go test ./internal/config -v` — PASS;
- `go test ./internal/pluginhost -v` — PASS, covering real subprocess replacement, in-flight generation pin/drain, broken rollback, same-version rebuild/cleanup, RPC timeout, and candidate allowlisting;
- `go test ./internal/agent -v` — PASS, covering strict schema and trailing-JSON rejection, wrapper event order/metadata, sequential multi-tool execution, tool-turn assistant-text suppression, and bounded fake-model Eino end-to-end event mapping;
- `go test ./internal/httpapi -v` — PASS, covering the whole-run deadline, disconnect/write-failure cancellation and runner join, terminal de-duplication/synthesis, Host/Origin/body/candidate guards, state/health semantics, and literal-loopback binding;
- `node --check web/app.js` — PASS;
- `node --test web/app.test.cjs` — PASS, 3 tests.

The fake-model test is deterministic application event-mapping evidence. It is not a live-provider test.

## Independent deterministic gates

The parent independently ran and reported the current candidate passing:

| Command | Reported result |
|---|---|
| `go test -race ./...` | PASS for all root packages |
| `go vet ./...` | PASS |
| `go build -o .runtime/luna ./cmd/luna` | PASS |
| `node --check web/app.js && node --test web/app.test.cjs` | PASS; 3 tests |
| Go formatting check | PASS |
| focused post-disconnect regression under the race detector, 50 repeated runs | PASS |

These gates supersede the implementation handoff's earlier “not yet run” blocker for deterministic verification.

Because the documentation files were untracked, final documentation validation used direct trailing-whitespace and whitespace-error scans rather than relying on `git diff --check` to inspect them.

## Live provider and plugin lifecycle evidence

One bounded verification run used the OpenAI-compatible model `deepseek-flash` at provider host `api.deepseek.com`:

- The provider selected `luna_text_transform` automatically; the request did not force provider-level `tool_choice`.
- With candidate `v1`, the tool returned `moon light`, and the application exposed the actual serving generation and plugin PID.
- Reloading to `v2` kept the root host PID at `1466972`, published a new generation with a different plugin PID, and returned `Luna · MOON LIGHT`.
- Reloading `broken` failed validation. The active `v2` generation remained published and callable afterward.
- No API key appeared in the retained evidence.

The host PID is recorded only as an ephemeral identifier from that verification run; it is not a stable configuration value.

## Real-browser evidence

Real headless Chromium interaction exercised the running UI and passed all of the following checks:

- successful `v1` interaction and visible tool metadata;
- successful reload and `v2` interaction;
- failed `broken` reload with the active `v2` path still usable;
- composer draft preservation while state polling and reload activity occurred;
- zero page errors;
- no horizontal overflow at desktop width or a 390 px viewport.

Desktop Preview also opened the live application at `http://127.0.0.1:40565` and read the actual visible model, provider, host, and plugin metadata. That URL is an ephemeral address from this verification run, not a launch default or stable endpoint.

## Final independent review

The final independent review passed with no security concerns or logic errors reported. This review and the completed live checks close the earlier provider/browser evidence gap; they do not expand the product boundaries described below or constitute a complete accessibility, cross-browser, load, or public-deployment assessment.

## Exact parent launch command

```sh
zsh -lc 'source ~/.secrets/llm-dsv4f.env && exec /home/j/probe/luna-agent/.runtime/luna -addr 127.0.0.1:0'
```

This command executes the trusted shell file `~/.secrets/llm-dsv4f.env`; it must not be used if that file is not trusted. No secret values belong in repository output or verification logs. The launched process prints the exact ephemeral `LISTEN_URL`. Any parent validation should be bounded and should terminate and wait for the server and owned plugin processes before completion.

## Limits

This is a local single-user preview. It has no persistent sessions, long-term memory, multi-agent orchestration, public cancel endpoint, arbitrary tool/plugin paths, runtime UI plugins, production authentication, or public-network deployment. Plugin reload replaces only the tool subprocess generation; it does not reload the core, model configuration, listener, or UI.

The root configures a default 60-second whole-run context deadline, 70-second HTTP read/write deadlines, and component deadlines of 60 seconds for plugin builds and 5 seconds for plugin startup/RPC. The run deadline bounds the full runner invocation while remaining below the server write deadline; parent cancellation may end a run sooner.
