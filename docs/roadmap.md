# Luna Agent roadmap

## Destination

`v0.1.0` publishes the existing kernel slice as a public repository: a Go agent kernel with a real
out-of-process tool plugin, verified hot reload, and a local browser workbench. The release is a
portability and presentation pass. It adds no new agent capability.

Feature expansion is deferred until after `v0.1.0` ships.

## v0.1.0 scope

### Blocking work

| # | Item | Why it blocks the release | Acceptance |
|---|---|---|---|
| 1 | Module path | `module luna-agent` is not a fetchable path, so nobody can `go get` it and the repo cannot be the source of truth for its own imports | Every import reads `github.com/Qaraku/luna-agent/...` and `go build ./...` passes |
| 2 | Start from any directory | `rootFromExecutable` assumes the binary sits at `<repo>/.runtime/luna`; `go run ./cmd/luna` resolves the root to a temporary build directory, so `plugins/` and `web/` are not found and the app cannot start | A clean clone starts with `go run ./cmd/luna`, serves `web/`, and builds plugin candidates |
| 3 | `LICENSE` | Without a license the code is all-rights-reserved by default and nobody may legally reuse it | MIT license at the repository root |
| 4 | Visitor-facing `README` | The current README is an internal tour of a slice; a visitor needs the problem, the design, and the quick start | Motivation, architecture diagram, quick start, verification summary |
| 5 | CI | A clean checkout must be provably green without the author's machine | `go test -race`, `go vet`, `go build`, `node --test` run on push |
| 6 | Host paths in documents | `README.md` and two `spikes/` documents embed `/home/j/...`, which is meaningless to a visitor and needlessly identifying | No absolute host path in tracked text files |
| 7 | Release | There must be a version to point at | `v0.1.0` tag with release notes |

### Pre-publish checks

- Every gate in `AGENTS.md` passes.
- A clean clone into a temporary directory starts, serves the UI, and survives a plugin reload.
- A secret scan over tracked files finds no credential material.
- No tracked build artifact, evidence directory, or captured log.

### Explicitly out of scope for v0.1.0

The `docs/architecture.md` non-goals still apply: multi-agent orchestration, persistent
conversations, durable run history, long-term memory, arbitrary shell/filesystem/network tools,
runtime UI plugins, a plugin marketplace, production authentication, and public deployment.

## After v0.1.0

Candidates, none committed. Each needs its own spec before implementation.

| Candidate | Value | Cost |
|---|---|---|
| Persistent sessions and run history | Turns the workbench from a demo into something usable daily | Durable store, restart semantics, migration policy |
| A second and third tool plugin | Proves the plugin boundary is a real extension point rather than one hard-coded case | Tool schema ownership, per-tool authorization |
| Context and memory strategy | The largest lever on answer quality | Retrieval design, privacy boundary |
| Runtime UI plugin loading | Completes the "everything is a plugin" principle on the front end | Registration, mount, unmount, resource cleanup protocols |
