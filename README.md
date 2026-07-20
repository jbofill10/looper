# looper

looper runs repeatable, step-based workflows ("loops") that pull work,
execute it, and repeat until there's nothing left to do. A loop's steps can
be shell scripts, prompts for a human, or invocations of an agentic coding
harness (e.g. `claude`) — either headless (non-interactive) or interactive
(a live pty session you can attach to). looper can run a loop directly in
your terminal, or hand it to a background daemon (`looperd`) that runs one
or more loops concurrently and that a terminal UI (`looper tui`) can attach
to and monitor.

## Concepts

- **Loop** — a named, ordered list of steps (`.looper/loops/<name>.yaml`),
  run repeatedly (one "iteration" per work unit) until a step signals there's
  no more work, an iteration is aborted, or `max_iterations` is reached.
- **Step** — one unit of work in a loop. Four types:
  - `script` — runs a shell command (`run:`).
  - `manual` — pauses and asks a human to advance, retry, or abort.
  - `headless` — sends a prompt to a coding harness non-interactively and
    inspects its output for sentinels (done / needs input / no work).
  - `interactive` — opens a live pty session with the harness (attachable
    via `looper attach` when run under the daemon).
- **Harness** — an external agentic coding tool (default: `claude`),
  configured in `~/.config/looper/config.yaml` (or
  `$XDG_CONFIG_HOME/looper/config.yaml`): the command lines used for
  interactive/headless invocation and the sentinel strings it emits.
- **Worker** — drives one loop's iterations, one step at a time, writing
  each iteration's state under `.looper/runs/<loop>/<iteration-id>/`
  (context, progress, event log, step logs, and a digest).
- **Daemon (`looperd`)** — a background process, reached over a Unix
  socket, that runs loops (potentially several workers concurrently) and
  lets `looper start`/`ls`/`stop`/`attach`/`tui` observe and control them
  without staying attached to a terminal.

## Install / build

Requires Go 1.26+.

```sh
go build -o looper .
```

Or install straight to your `$GOBIN`:

```sh
go install github.com/jbofill10/looper@latest
```

## Quickstart

```sh
# Create a loop with the guided, interactive builder.
looper new my-loop

# Run it in the foreground, in this terminal, one worker, no daemon.
looper run my-loop

# Or hand it to the daemon (auto-spawned if not already running) and
# stream its progress; Ctrl-C detaches without stopping the run.
looper start my-loop

# See what's running.
looper ls

# Reattach to a run's live interactive step.
looper attach <run-id>

# Stop a run.
looper stop <run-id>

# Launch the fleet/focus terminal UI to monitor and control loops.
looper tui

# Resume the most recently interrupted iteration of a loop (e.g. after a
# crash or Ctrl-C mid-iteration).
looper resume my-loop
```

Running bare `looper` with no subcommand launches the TUI.

## Commands

| Command | Purpose |
|---|---|
| `looper new [name]` | Guided builder; writes `.looper/loops/<name>.yaml`. |
| `looper edit <name>` | Guided builder, pre-populated from an existing loop. |
| `looper run [name] [--file]` | Run a loop in-process, single worker, in this terminal. |
| `looper resume [name] [--file]` | Resume the most recent interrupted iteration of a loop. |
| `looper start [name] [--file] [--concurrency N]` | Start a loop in the daemon and stream its progress. |
| `looper ls` | List runs known to the daemon. |
| `looper stop <run-id>` | Stop a running loop in the daemon. |
| `looper attach <run-id>` | Attach to a run's live interactive session. |
| `looper tui` | Launch the fleet/focus terminal UI. |
| `looper ping` | Ensure the daemon is running (auto-spawning it) and print its version. |
| `looper shutdown` | Ask a running daemon to stop gracefully. |
| `looper daemon --socket <path>` | Run the daemon in the foreground (what auto-spawn invokes; also useful for debugging). Ctrl-C (SIGINT) or SIGTERM shuts it down cleanly and removes its socket. |
| `looper version` | Print the looper/looperd version. |

Most commands accept `--socket <path>` to talk to a non-default daemon
socket (default: `$XDG_RUNTIME_DIR/looper.sock`, or an OS-appropriate
fallback — see `client.SocketPath()`).

## Resume semantics

Every iteration persists its state as it runs: `context.json` (the
iteration's KV variables), `progress.json` (which steps have completed and
whether the iteration finished), an `events.jsonl` log, per-step logs, and a
`digest.md` summary — all under
`.looper/runs/<loop>/<iteration-id>/` (or
`.looper/runs/<loop>/<worker-id>/<iteration-id>/` for a multi-worker run).

`looper resume <loop>` finds the most recent iteration directory that isn't
marked done (or has no progress.json yet at all) and continues it: steps
already completed are skipped (their outcome is reported as
`resumed-skip`), and execution picks back up at the first incomplete step,
reusing that iteration's persisted context (including its `WORKDIR`). The
step that was interrupted runs again from the beginning — its side effects
may repeat. Subsequent iterations in that same run start fresh, as usual.
If there's nothing to resume, it prints `nothing to resume` and exits 0.

## Loop YAML schema

```yaml
name: my-loop            # required
workspace: shared         # shared|worktree (optional)
concurrency: 1             # workers to run in parallel (default 1)
max_concurrency: 1         # ceiling if concurrency is scaled up (default = concurrency)
max_iterations: 0          # 0 = unbounded
task_var: TASK_ID          # the output var identifying the current work unit (default TASK_ID)
schedule:                 # optional repeating trigger; exactly one of every/at/cron
  every: 15m               # or: "2h", "1h30m" — any time.ParseDuration string
  # at: ["09:00", "14:00", "20:00"]   # daily times, 24-hour HH:MM
  # cron: "0 9 * * 1-5"               # raw 5-field cron expression
steps:
  - name: get-task
    type: script
    run: "..."               # shell command
    signals_no_work: true    # exit 78 (or the harness no-work sentinel) ends the loop
    outputs: [TASK_ID]       # vars this step's $LOOPER_OUTPUT may set, made available to later steps

  - name: implement
    type: headless
    prompt: "..."
    harness: claude           # optional; defaults to the loop's/global default harness
    on_fail: ask              # ask|retry|abort (default ask)

  - name: review
    type: interactive
    prompt: "..."

  - name: confirm
    type: manual
```

Step types: `script`, `manual`, `headless`, `interactive` (see Concepts,
above). `on_fail` controls what happens when a `script`/`headless` step
fails: `ask` (default) prompts, `retry` re-runs the step, `abort` ends the
iteration.

A loop with a `schedule` is fired by looperd itself, as a fresh one-off run
(like `RunLoopOnce`), each time the schedule ticks — for as long as
looperd is running. If a run is already active for that loop when a tick
fires, the tick is skipped rather than stacked. Schedules use the daemon
process's local time zone and don't catch up on ticks missed while the
daemon was down.

## Configuration

Global harness configuration lives at
`$XDG_CONFIG_HOME/looper/config.yaml` (or `~/.config/looper/config.yaml`):

```yaml
default_harness: claude
harnesses:
  claude:
    interactive: ["claude"]
    headless: ["claude", "-p", "{{PROMPT}}"]
    sentinels:
      needs_input: "@@LOOPER:NEEDS_INPUT@@"
      done: "@@LOOPER:DONE@@"
      no_work: "@@LOOPER:NO_WORK@@"
```

If the file is absent, or omits harnesses, looper falls back to its
built-in `claude` default.

## More detail

See `docs/superpowers/specs/` for the original design write-ups this
codebase was built from.
