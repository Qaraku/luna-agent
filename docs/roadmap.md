# Luna Agent roadmap

## Destination

A local agent worth using every day: more than one real tool, conversations that survive a restart,
an explicit context strategy, and a browser front end that can be extended at runtime.

## Version numbers

Releases stay in `0.x` while this is an experimental, single-user kernel whose interfaces can still
change. `1.0.0` is deliberately unscheduled: nothing here is a stable-interface promise, and the
README's boundaries — loopback only, one run at a time, no authentication or tenant isolation — are
as much part of the current version as its features are.

| Version | What it contains |
|---|---|
| `v0.1.0` | the plugin kernel slice: subprocess tools, validated reload, drain and rollback. It shipped before this roadmap's slices and is no longer tracked here |
| `v0.2.0` | S1–S5 below, plus the defects the first acceptance pass found. Each slice's introducing commits are recorded in the scope table below |

## v0.2.0 scope

| Slice | Content | Depends on | Introduced by | Status |
|---|---|---|---|---|
| S1 | A second tool plugin: bounded file reading | — | `55a64e6` (core), `3372ce6` (web) | in `v0.2.0` |
| S2 | Persistent sessions and run history | — | `60de6ff` (core), `85a441d` (web) | in `v0.2.0` |
| S3 | Context and memory strategy | S2 | `6629806` (core) | in `v0.2.0` |
| S4 | Runtime UI plugin loading | — | `5508cfb` (core), `edce312` (web) | in `v0.2.0` |
| S5 | Memory you can see and retract | S3 | `c037517` (core), `99e321a` (web) | in `v0.2.0` |

`v0.2.0` also carries the fixes the first acceptance pass produced: `6ed5315` (classify plugin
infrastructure failures by sentinel), `f1839ed` (return a tool refusal to the model instead of ending
the run) and `9b39721` (the memory copy claiming a fact can never be removed). `v0.1.0` shipped one
plugin-backed tool — `text_transform`, from a flat `plugins/<candidate>/` layout — so
`luna_read_file`, sessions, memory and runtime UI plugins are all `v0.2.0` additions.

### Slice acceptance

- **S1** — a second model-visible tool, `luna_read_file`, bounded by a read root: absolute paths,
  traversal, escapes outside the root, and symlink escapes are rejected on the host side; there is a
  single-read size cap; only text is returned. The allowlist holds two tools, and reload,
  generation pinning, drain and rollback all work for both of them.
- **S2** — conversations and run records survive a process restart and a page reload.
- **S3** — the rules that decide what enters the model's context are explicit and unit-tested.
- **S4** — front-end contribution points register, mount, unmount, and release their resources.
- **S5** — `GET /api/memory` lists the facts in effect and the facts that were retracted, and
  `POST /api/memory/retract` takes exactly the fact named by its text and its timestamp out of the
  effective set, refusing one that is not in effect. Retraction is an appended record, so the store
  stays append-only, the file stays bounded even when facts are retracted in a loop, and the model's
  own rights are unchanged: it can still add a fact and nothing else.

## How this is being built

One slice at a time. A slice is verified by compilation plus focused unit tests; the end-to-end pass
against a live provider, and the browser, happen at the boundary of a tagged release rather than per
slice, so a tag points at the tree the acceptance actually ran against. An unreleased intermediate
slice that fails end to end is corrected by the slices that follow it rather than by stopping the
line.

## Non-goals

Multi-agent orchestration, arbitrary shell execution, unbounded filesystem access, a plugin
marketplace, production authentication, tenant isolation, public deployment, cross-origin API
access, hidden reasoning capture, and retries that could duplicate model or tool effects.
