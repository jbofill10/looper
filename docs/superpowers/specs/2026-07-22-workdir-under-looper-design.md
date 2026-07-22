# Default execution workdir moves under `.looper/`

## Problem

`Worker.Workdir` — the directory a `script`/`headless`/`interactive` step
actually runs in (exposed to steps as the `WORKDIR` context var) — defaults
to `os.Getwd()` at CLI invocation time, i.e. the project's repo root
(`cli/run.go:99-106`). Every loop and every worker on a given project
currently shares this one directory.

In practice, steps that write relative-path output (e.g. `get-cfa-tasks`'s
`run: acli jira workitem search ... > tasks.json`) land those files directly
in the repo root: `tasks.json`, `tasks-digest.md`, `task-CLS-1865.json`,
`repos-CLS-1865.json`, `repos-digest-CLS-1865.md` all showed up as untracked
clutter in `looper`'s own working tree from running `get-cfa-tasks` and
`cfa-dev-loop` against it.

Confirmed via `cfa-dev-loop.yaml` that this scratch directory is never where
real project work happens — steps like `pick-repos` and `plan-task` operate
against absolute paths under `~/git/<repo>`, selected dynamically. `WORKDIR`
is purely staging space for task metadata (JSON/plan/digest files). Moving
it is safe.

## Design

**Default workdir becomes `.looper/work/<loop-name>/<worker-id>/`.**

- Computed from fields already available at `Worker` construction:
  `Worker.BaseDir` (the `.looper` dir), `Worker.Loop.Name`, `Worker.ID`.
- Mirrors the existing `.looper/runs/<loop-name>/<worker-id>/` layout.
- Created on first use (`os.MkdirAll`) — the repo root existing was always
  a given; this directory is not, so callers must create it rather than
  assume it.
- **Persistent across iterations**, matching today's behavior exactly.
  `runner/worker.go`'s resumed-iteration path only (re)sets `WORKDIR` if it
  is unset (lines 231-233); a fresh iteration always sets it to
  `w.Workdir` (line 243). Neither of these changes — only where `w.Workdir`
  itself points.
  - This persistence is intentional and load-bearing: loops like
    `cfa-dev-loop` rely on state (checkouts, in-progress branches, deps)
    surviving between iterations of the same worker.
  - It also means sequential runs of the same loop's same worker slot
    (worker IDs are deterministic — `w1`, `w2`, ... per concurrency slot,
    `daemon/manager.go:308` — not unique per invocation) reuse the same
    directory over time, same as today. A loop author wanting a clean
    slate per run needs an explicit setup step, same requirement as today.
  - Per-run *provenance* of what a step produced is a separate, already
    working mechanism: `runner/digest.go`'s `captureDigest` copies each
    step's declared digest-output file into
    `.looper/runs/<loop>/<worker>/<run-id>/steps/<step>.digest.md` at the
    time the step runs, independent of what happens to the live workdir
    afterward. This spec does not touch that.

**Call sites:**

- `cli/run.go`: stop threading `os.Getwd()` into `RunOptions.Workdir` /
  `Worker.Workdir` for the default case. `Worker.Workdir` is computed
  internally (or lazily resolved) from `BaseDir`/`Loop.Name`/`ID` instead
  of being injected from outside.
- `daemon/manager.go` / `daemon/catalog.go`: same — the `workdir` string
  currently threaded through `registryEntry`/`StartLoop` for this purpose
  goes away in favor of the internally-computed path.
- `runner/worker.go`: `Worker.Workdir` becomes derived rather than a
  required external input the same way `runIteration`'s directory
  computation (`w.BaseDir`, `w.Loop.Name`, `w.ID`, lines 235-238) already
  works.

**`.gitignore`:** add `.looper/work/`.

**Migration:** the stray files already sitting in this repo's root
(`tasks.json`, `tasks-digest.md`, `task-CLS-1865.json`,
`repos-CLS-1865.json`, `repos-digest-CLS-1865.md`) get deleted manually as
part of landing this change — not a code concern.

## Out of scope

- `workspace: worktree` / git-worktree isolation for git-based loops. The
  `Loop.Workspace` schema field (`config/loop.go:50`) stays parsed-but-unused;
  building the actual worktree convenience is a separate, later effort.
- Wiring up `runctx.RunContext.ArtifactsDir()` (currently dead — defined,
  never called outside tests) to capture per-run snapshots of arbitrary
  `outputs:` files, not just `digest:` files. Known gap, not this task.
- Any protection against two concurrent runs of the same loop colliding on
  worker IDs (`daemon/manager.go`'s `StartLoop` has no dedup-by-loop-name
  check anywhere; confirmed pre-existing, unrelated to this change).

## Testing

- Unit test: a `Worker` with `BaseDir`, `Loop.Name`, `ID` set produces the
  expected `.looper/work/<loop>/<id>/` path and creates it before running a
  step.
- Unit test: two `Worker`s with different `Loop.Name` or `ID` get distinct
  workdirs (no accidental sharing across loops/workers).
- Existing `runner`/`daemon`/`cli` tests that assert on `WORKDIR`/`Workdir`
  behavior updated to reflect the new default; resumed-iteration reuse
  semantics covered by existing tests should continue to pass unchanged.
