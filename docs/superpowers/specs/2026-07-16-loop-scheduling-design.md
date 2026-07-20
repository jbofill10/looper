# Loop scheduling: cron-style triggers for loops

## Problem

Today a loop only runs when something explicitly starts it: `looper start`,
`RunLoopOnce`, or the continuous "enabled" toggle (which runs it forever,
iterating until stopped). There is no way to say "run this every 15
minutes" or "run this at 9am and 2pm every day" without a user or external
cron job manually invoking `looper start`/`RunLoopOnce` — the prior Loops
catalog design explicitly called this out as out of scope.

## Goal

Let a loop declare a repeating schedule (interval, fixed daily times, or a
raw cron expression) in its YAML definition. The daemon runs the loop as a
fresh one-off run (like `RunLoopOnce`) each time the schedule fires, for as
long as looperd is running, surviving daemon restarts.

## Out of scope

- One-time/non-repeating schedules (e.g. "March 5th only").
- Per-timezone schedules — all schedules use the daemon process's local
  time.
- Catch-up/backfill for missed ticks while the daemon was down — a missed
  tick is simply not run.

## Schedule spec (loop YAML)

`config.Loop` gains an optional `Schedule` field:

```go
type Schedule struct {
    Every string   `yaml:"every,omitempty"` // duration shorthand: "15m", "2h"
    At    []string `yaml:"at,omitempty"`    // daily times: ["09:00","14:00","20:00"]
    Cron  string   `yaml:"cron,omitempty"`  // raw 5-field cron expression
}
```

Exactly one of `Every`, `At`, `Cron` may be set on a given loop.
`Loop.Validate()` rejects a `Schedule` with none or more than one set, and
rejects an unparseable `Every`/`At`/`Cron` value.

Example loop file:

```yaml
name: nightly-report
schedule:
  at: ["21:00"]
steps: [...]
```

### Normalization

All three forms normalize to a `github.com/robfig/cron/v3` spec string,
used as the actual scheduling engine (a well-tested standard library,
rather than a hand-rolled ticker):

- `every: "15m"` → `@every 15m0s` (parsed with `time.ParseDuration`, then
  formatted back so `cron` accepts it)
- `at: ["09:00", "14:00"]` → `0 9,14 * * *` (minute-granularity; seconds
  are not supported)
- `cron: "0 9 * * 1-5"` → passed through verbatim

## Trigger semantics

Each firing behaves like the existing `RunLoopOnce`: a fresh run starts,
executes one iteration (`max_iterations` forced to 1), and ends. It does
not touch the loop's continuous `enabled` flag or registry entry.

Before firing, the daemon checks whether the loop already has an active
run in that `base_dir` (`Manager.activeRun`, already used by
rename/delete). If so — either a prior scheduled run still in flight, or
the loop separately running continuously via `enabled` — the tick is
skipped, not queued or stacked. A skipped tick is not an error; it is
logged for visibility (surfaced as a future TUI/log line, not a new RPC
error).

## Daemon internals

- `Manager` gains a `scheduler *cron.Cron` (started once, at `NewManager`
  time or lazily on first schedule) and an internal map of registered
  entries keyed by `(baseDir, loopName)` → the entry's cron ID and its
  last-known normalized spec string.
- The daemon-wide registry file (`daemon/registry.go`) gains a top-level
  `knownProjects []string`: every `base_dir` ever passed to `ListLoops` is
  recorded here (append-if-absent). This lets a restarted daemon know
  which project directories to rescan for schedules without requiring a
  client to call `ListLoops` again first — mirroring how `AutoResume`
  already replays the `enabled` registry entries at startup.
- `registryEntry` gains a `ScheduleEnabled bool`, defaulting to `true` the
  first time a given loop's schedule is discovered (i.e. absence of an
  entry means "enabled" for a loop that has a `Schedule`, matching how
  discovery works — unlike the continuous `enabled` flag, whose absence
  means disabled).
- A background rescan goroutine runs every 30s (and once at daemon
  startup, after loading `knownProjects`): for each known project dir, it
  loads every `loops/*.yaml` (via the existing lenient loader), and for
  each loop with a `Schedule`:
  - not yet registered → add a cron entry
  - registered but the normalized spec changed → remove and re-add
  - registered but `ScheduleEnabled` is now false, the loop file is gone,
    or its `Schedule` was removed → remove the cron entry
  This treats the filesystem as the source of truth for schedule
  definitions, consistent with `ListLoops`'s existing scan-based approach,
  and requires no explicit notification from the builder/TUI's file-write
  paths.
- The cron job registered for `(baseDir, loopName)` calls the same
  internal path `RunLoopOnce` uses, after the active-run skip check above.

## RPC / CLI / TUI surface

- New RPC `SetScheduleEnabled(loop_name, base_dir, workdir, enabled) returns (SetScheduleEnabledResponse)`,
  mirroring `SetLoopEnabled`'s shape but toggling `ScheduleEnabled` instead
  of `Enabled`, with no run started/stopped as a side effect (a schedule
  toggle only affects future ticks).
- `ListLoopsResponse`'s `LoopInfo` (and `daemon.LoopSummary`) gain:
  - `schedule_enabled bool`
  - `next_run` (RFC3339 timestamp string, empty if the loop has no
    schedule or its schedule is disabled), read from the cron entry's
    `Next` field.
- Loops catalog in the TUI (`tui/model.go`) shows a schedule indicator and
  next-run time per loop row, with a new keybind `s` to toggle
  `ScheduleEnabled` — parallel to the existing `t` (continuous
  enable/disable).

## Error handling

- Invalid `Schedule` values fail `Loop.Validate()` at load/save time (same
  path as any other invalid step/loop field) — a loop with a bad schedule
  simply isn't picked up by the rescan (consistent with how `ListLoops`
  already skips unparseable loop files).
- A `RunLoopOnce`-equivalent triggered by a schedule that itself fails
  (script error, etc.) surfaces the same way any other run failure does
  today — via the run's `state`/`run_done` updates a subscribed client
  would see, with no special-casing for scheduled runs.

## Testing

- Shorthand→cron normalization: `every`/`at`/`cron` → correct cron spec
  string, including edge cases (`at` with multiple times, `every` with
  compound durations like `"1h30m"`).
- `Loop.Validate()`: zero-set, multi-set, and malformed `Schedule` all
  rejected with a clear error.
- Skip-if-active-run behavior: a tick while a run is active does not start
  a second run.
- Rescan add/update/remove diffing, using an injectable rescan interval
  (not real 30s waits) and a fake clock/short cron specs so tests run fast.
