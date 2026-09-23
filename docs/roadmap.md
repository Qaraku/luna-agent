# Luna Agent roadmap

## Destination

`v1.0.0` turns the `v0.1.0` kernel slice into a local agent worth using every day: more than one real
tool, conversations that survive a restart, an explicit context strategy, and a browser front end
that can be extended at runtime.

`v0.1.0` shipped first as a portability and presentation pass that added no agent capability. It is
tagged and released, so this roadmap no longer tracks it.

## v1.0.0 scope

| Slice | Content | Depends on | Status |
|---|---|---|---|
| S1 | A second tool plugin: bounded file reading | — | on `main` |
| S2 | Persistent sessions and run history | — | on `main` |
| S3 | Context and memory strategy | S2 | not started |
| S4 | Runtime UI plugin loading | — | not started |

### Slice acceptance

- **S1** — a second model-visible tool, `luna_read_file`, bounded by a read root: absolute paths,
  traversal, escapes outside the root, and symlink escapes are rejected on the host side; there is a
  single-read size cap; only text is returned. The allowlist holds two tools, and reload,
  generation pinning, drain and rollback all work for both of them.
- **S2** — conversations and run records survive a process restart and a page reload.
- **S3** — the rules that decide what enters the model's context are explicit and unit-tested.
- **S4** — front-end contribution points register, mount, unmount, and release their resources.

## How this is being built

One slice at a time. Each slice is verified by compilation plus focused unit tests; end-to-end
verification against a live provider happens once, at the `v1.0.0` boundary. An unreleased
intermediate slice that fails end to end is corrected by the slices that follow it rather than by
stopping the line.

## Non-goals for v1.0.0

Multi-agent orchestration, arbitrary shell execution, unbounded filesystem access, a plugin
marketplace, production authentication, tenant isolation, public deployment, cross-origin API
access, hidden reasoning capture, and retries that could duplicate model or tool effects.
