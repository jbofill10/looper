# Step digests: design

## Problem

Several existing loops (e.g. `.looper/loops/cfa-dev-loop.yaml`) already have
headless/interactive steps write a short markdown "digest" summarizing what
they did, so a later step's prompt can read it for context (e.g.
`REPOS_DIGEST_FILE`, `PLAN_DIGEST_FILE`). Today this is purely a naming
convention on top of the existing declared-`outputs` mechanism — looper has
no structural notion that a given output is "the digest" for a step.

Separately, looper already writes an iteration-level `digest.md` rollup (a
`- step → outcome` list, see `runner/worker.go`), but that's an auto-generated
pass/fail summary, not step-authored content, and there's no way to view it
(or any step's digest) from the fleet TUI — the TUI is live-only, with no
history browsing of past runs at all.

This spec formalizes step-authored digests as a first-class concept, persists
them per step per iteration, and adds TUI support for browsing past runs and
viewing each step's digest.

## Goals

- A step can declare that one of its outputs is its digest.
- Looper captures that digest's content into the run dir automatically.
- From the fleet TUI, a user can browse a loop's run history (including runs
  from before the current daemon session) and drill into a specific
  iteration to view each step's digest.

## Non-goals

- Editing or deleting digests from the TUI.
- Changing the existing iteration-level `digest.md` rollup — it remains
  as-is and is a separate concept from step digests.
- Live-streaming digest content while a step is still running; the TUI reads
  whatever's on disk when the view is opened.
- Cross-run search/filtering of digests.

## 1. Config schema

Add an optional `digest` field to `Step` (`config/loop.go`):

```yaml
- name: pick-repos
  type: headless
  prompt: |
    ...
    Also write a short digest of your reasoning to a file at
    $(pwd)/repos-digest-{{TASK_ID}}.md...
  outputs:
    - REPOS_FILE
  digest: REPOS_DIGEST_FILE
```

`digest` names an output var whose captured value is a path to a markdown
file. It implicitly declares that var as an output — it does not need to
also appear in `outputs:`. `Step.Validate` requires that `digest`, if set,
does not collide with a name already listed in `outputs` (no duplicates).

## 2. Capture & storage

`captureOutputs` (`runner/script.go`) already resolves declared output vars
from the step's `$LOOPER_OUTPUT` file into the run context. When
`step.Digest != ""`, after resolving it like any other output var, the
runner reads the file at that path and copies its content to
`steps/<step-name>.digest.md` in the run dir — alongside the existing
`steps/<step>.log` and `steps/<step>.outputs`.

This gives every step digest a stable, run-dir-local home independent of
where the original scratch file lived (which may be cleaned up later), and
gives the history scanner (below) a predictable filename to look for. A
missing digest file (var unset, or file doesn't exist) is not an error —
the step simply has no digest for that iteration.

## 3. History data model (disk scan)

A new function walks `.looper/runs/<loop>/[<worker>/]<iteration>/` on demand
(no persistent index or daemon-resident cache — it's a plain directory walk)
and builds one `RunRecord` per iteration directory:

- Iteration ID (encodes start timestamp, e.g. `20260717T134719-001`)
- Worker ID, if the loop uses per-worker subdirs
- Status, derived from `progress.json` (`Done`) and presence of
  `digest.md`: running / completed / aborted / no-work
- The loop's steps in config order, each annotated with whether
  `steps/<step>.digest.md` exists for that iteration

Currently-live iterations tracked in the daemon's in-memory state are merged
into the same list (matched by run/worker/iteration ID) so in-progress
iterations appear too, marked as running.

## 4. TUI: navigation and rendering

Two new view states are added to `tui/model.go`'s `viewKind` enum:

- **`viewRuns`** — a loop's run history. Entered by pressing `h` on a loop
  row in the Loops catalog (alongside the existing `t`/`o`/`g`/`x`/`R`/`D`
  keys). Lists iterations newest-first: iteration ID/timestamp, worker, and
  status indicator (running / done / aborted / no-work). `esc` returns to
  the Loops catalog.
- **`viewDigest`** — a selected iteration's steps. Lists the loop's steps in
  config order; steps with a captured digest are marked and selectable,
  steps without one are shown but greyed out/unselectable. Selecting a step
  renders `steps/<step>.digest.md` in a scrollable pane, styled via
  `glamour` (new dependency) for markdown rendering. `esc` returns to
  `viewRuns`.

Both views read from disk fresh each time they're opened — no caching — so
reopening a still-running iteration reflects newly-completed steps' digests.

## Testing

- `config`: `Step.Validate` rejects `digest` duplicating an `outputs` entry;
  accepts `digest` alone without a matching `outputs` entry.
- `runner`: a step with `digest` set produces `steps/<step>.digest.md` with
  the referenced file's content; a step without `digest`, or whose digest
  var is unset/missing file, produces no such file and no error.
- History scan: builds correct `RunRecord`s from a fixture run-dir tree
  (completed, aborted, no-work, and running-but-incomplete iterations).
- TUI: `h` on a loop row transitions to `viewRuns`; selecting an iteration
  transitions to `viewDigest`; `esc` unwinds one level at a time.
