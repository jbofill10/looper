# TUI loops catalog: enable/disable, run-once, inline step editing

## Problem

The Fleet view (`tui/model.go`) only shows *active runs/workers*, streamed
live from the daemon. There is no way, from the TUI, to see which loops
exist in `.looper/loops/`, control whether a loop runs continuously, run one
off, or edit a loop's steps without going through the separate "new loop"
naming/builder flow. Loop enablement also does not survive a daemon
restart today — a loop only runs because someone explicitly started it.

## Goal

Add a **Loops** section to the Fleet view: a collapsible list of every
configured loop, each togglable between running-continuously ("enabled")
and stopped, with a run-once action, graceful/hard stop, rename/delete, and
inline step add/edit/delete/reorder — reusing the existing step-authoring
builder engine rather than building new step-editing logic.

## Out of scope

- External triggers (webhooks/cron) driving loop starts.
- Live rename/delete of a loop that has an active run (both are simply
  rejected while running).
- Any change to the existing flat worker table, `n` new-loop flow, or
  focus/attach views.

## Data model & persistence

looperd is a single per-user process shared across however many project
directories invoke it (one Unix socket per uid — see `client.SocketPath()`);
every RPC that touches a loop already takes an explicit `base_dir`/`workdir`,
since the daemon has no built-in notion of "the current project." A
per-project `<base_dir>/state.json` would therefore be invisible to the
daemon on its own restart — it would have no way to discover which project
directories to go re-read.

Instead, a single **daemon-wide registry file** lives next to the daemon's
socket (i.e. resolved the same way `client.SocketPath()` resolves the
socket path — `$XDG_RUNTIME_DIR/looper-state.json` or the `os.TempDir()`
per-user fallback), keyed by `(base_dir, loop_name)`:

```json
{
  "loops": {
    "/Users/juan/proj1|jira-tracker": {
      "baseDir": "/Users/juan/proj1/.looper",
      "workdir": "/Users/juan/proj1",
      "loopName": "jira-tracker",
      "enabled": true
    }
  }
}
```

- Loop YAML files remain pure workflow definitions; enablement is daemon
  runtime state, not loop config.
- On daemon startup, after loading the registry, the daemon calls
  `StartLoop` for every entry marked enabled that isn't already running
  (auto-resume) — it already has that entry's `base_dir`/`workdir`, so no
  project discovery is needed.
- Loops absent from the registry are treated as disabled (default).

### Run once

`RunLoopOnce` starts a run with `max_iterations` forced to `1` for that
invocation only. It does not read or write the registry and has no effect
on the loop's enabled flag.

### Graceful stop vs hard abort

Two distinct stop paths, both operating on a run:

- **Graceful** — sets a new stop-flag on the run's worker(s), checked only
  at the iteration boundary in `Worker.run()`'s `for` loop (alongside the
  existing `w.ctxErr()` check). The in-flight iteration's steps run to
  completion; the loop exits before starting the next iteration. The
  run's context is *not* cancelled, so an in-flight interactive/headless
  step is not interrupted. This is what disabling a running loop triggers.
- **Hard abort** — the existing `StopLoop` behavior: cancels the run's
  context immediately, which can interrupt an in-flight step at its next
  cancellation checkpoint. Unchanged.

## Daemon RPCs

Added to `proto/looper.proto`'s `Looper` service:

- `ListLoops(ListLoopsRequest{base_dir}) returns (ListLoopsResponse)` —
  scans `<base_dir>/loops/*.yaml`, cross-referencing the daemon-wide
  registry and active runs. Returns, per loop: name, file path, enabled
  bool, step names in order, and the active `run_id` if currently running
  (empty otherwise).
- `SetLoopEnabled(SetLoopEnabledRequest{loop_name, base_dir, workdir, enabled}) returns (...)`
  — persists the flag (plus base_dir/workdir, needed for auto-resume) to
  the registry. Enabling a not-yet-running loop starts it; disabling a
  running loop triggers a graceful stop.
- `RunLoopOnce(RunLoopOnceRequest{loop_name, base_dir, workdir}) returns (RunLoopOnceResponse{run_id})`
  — see above.
- `StopLoopGraceful(StopLoopGracefulRequest{run_id}) returns (...)` — sets
  the graceful stop-flag on the run. Existing `StopLoop` remains the
  unchanged hard-abort path.
- `RenameLoop(RenameLoopRequest{loop_name, new_name, base_dir}) returns (...)` /
  `DeleteLoop(DeleteLoopRequest{loop_name, base_dir}) returns (...)` —
  rename/delete the loop's YAML file and its registry entry (if any). Both
  return an error if the loop currently has an active run.

`ListRuns` is unchanged (active runs only) — it remains the source for the
existing flat worker table; `ListLoops` is the new source the Loops tree
renders from.

## TUI: tree layout

The Fleet view gains a Loops section above the existing (unchanged) worker
table:

```
looper · 2 runs · 1 NEED YOU

Loops
▸ jira-tracker        [on]  running (run-004)
  ▾ jira-tracker                      <- expanded
      1. fetch-ticket
      2. work-on-task      ← selected step row
      3. open-pr
  headless-example     [off]
  new-1                 [off]

Workers
  w-1      jira-42   work-on-task   ⚙
  w-2      jira-51   open-pr        ⏸ NEEDS YOU

[up/down] move  [space] expand/collapse  [t] toggle enabled  [o] run once
[g] graceful stop  [x] abort  [c] add step  [e] edit step  [d] delete step
[shift+up/down] reorder step  [R] rename  [D] delete loop  [n] new loop  [q] quit
```

- The Model's cursor moves between loop rows and, when a loop is expanded,
  its step rows.
- Expanding a loop instantiates a `builder.Model` scoped to that loop's
  file (same constructor used by the `n` flow), but its step list renders
  *inline*, indented under the loop's row, instead of taking over the
  whole screen. Add/edit/delete/reorder keys (`c`/`e`/`d`/`shift+up`/
  `shift+down`) forward to that embedded `builder.Model` while the loop is
  expanded and its step-list has focus — reusing its existing logic
  (YAML load/save, authoring-session launch via `AuthorFn`,
  `SessionDoneMsg` handling) unmodified.
- Loop-level actions: `t` toggle enabled (`SetLoopEnabled`), `o` run once
  (`RunLoopOnce`), `g`/`x` graceful-stop/hard-abort (only meaningful when
  the loop has an active run), `R` rename, `D` delete (with a confirm
  prompt, mirroring the existing naming-stage input pattern).
- The existing `n` new-loop flow (naming prompt → full-screen builder) is
  unchanged.

## Testing

- `runner`: new tests for the graceful stop-flag — a running iteration
  completes normally, the next iteration does not start, context is not
  cancelled.
- `daemon`: tests for `ListLoops` scanning/cross-referencing, auto-resume
  on startup from `state.json`, `SetLoopEnabled` start/stop side effects,
  `RunLoopOnce` forcing `max_iterations=1`, rename/delete rejecting while
  running.
- `tui`: model tests for tree navigation (expand/collapse, cursor movement
  across loop/step rows), each loop-level key dispatching the right RPC
  call via injected fn fields (mirroring how `RespondFn`/`AttachFn` are
  tested today), and inline step add/edit/delete/reorder against a fake
  loop file.
