# looper — Design Spec

**Date:** 2026-07-15
**Status:** Approved design, pre-implementation

## 1. Summary

`looper` is a Go tool for **loop abstraction of workflows**. A user defines a
**loop** — an ordered list of **steps** — and looper runs that loop as a pool of
concurrent **workers**, each pulling a distinct unit of work and driving it
through every step to completion, then looping for the next unit. The canonical
example is a `dev-loop`: `get-task → plan → implement → test → pr`, running until
no tasks remain.

Steps run via a configurable **harness** (default: `claude`, interactive). looper
supervises interactive agent sessions in a PTY and reflects their live state
(working / needs-human / awaiting-approval) on a TUI dashboard, letting the user
attach to a session, act, and detach — without the loop losing its place.

looper is split into a **daemon** (`looperd`, owns all state and long-lived work)
and a **client** (`looper`, the TUI front-end). This lets loops run in the
background, survive client disconnects, and — in a later version — run on a
schedule.

## 2. Core Vocabulary

- **Loop** — a named, ordered list of steps (e.g. `dev-loop`).
- **Step** — one unit of work in a loop. One of four types: `interactive`,
  `headless`, `script`, `manual`.
- **Worker** — an independent executor that pulls one work unit and drives it
  through all steps. A loop runs `concurrency` workers in parallel.
- **Work unit** — the thing a worker processes in one pass (a ticket, an issue,
  a queue item). Produced by the loop's first step. No two workers ever share a
  work unit.
- **Iteration** — one worker's full pass through all steps for one work unit.
- **Run context** — per-iteration KV store + artifacts, owned by looper.
- **Harness** — how looper launches an agent (invocation modes + hook wiring).
  Default and only built-in in v1: `claude`.

## 3. Execution Model

**Iterate over work units.** Each worker loop:

```
loop:
    unit = get-task            # first step; may signal NO_WORK
    if NO_WORK: worker goes idle
    for step in remaining steps:
        run step in WORKDIR
        resolve outcome → advance | retry | abort
    persist iteration digest
    repeat
```

- **Concurrency**: a loop declares `concurrency: N` (default 1),
  `max_concurrency` bounds runtime scaling. looper runs N workers; each calls
  `get-task` independently and receives a **distinct** unit. looper tracks which
  units are claimed by live workers so the same unit is never handed out twice.
- **Loop termination**: the loop ends when `get-task` signals no-work **and** all
  workers are idle. Also: manual stop, and optional `max_iterations`.
- When `concurrency: 1`, the client skips the fleet view (§7) and drops straight
  into the single worker's focus view.

## 4. Step Types & Outcome Resolution

| Type | What it does | Outcome signal |
|------|--------------|----------------|
| `interactive` | Live harness session in a PTY, with dashboard state + attach/detach | Sentinel from Stop hook + human approve/retry/abort at the pause |
| `headless` | Non-interactive harness run (`claude -p`) to completion | Exit code + optional verification |
| `script` | Shell command / script | Exit code (0 = advance; nonzero → on_fail policy) |
| `manual` | Human does something out-of-band, marks done in the client | Human action |

- **`on_fail` policy** (script/headless): `retry | abort | ask` (default `ask`).
- **Loop-terminating signal**: the first step may declare `signals_no_work: true`;
  an empty result (reserved exit code, or `@@LOOPER:NO_WORK@@` sentinel) ends the
  loop for that worker.

## 5. Interactive Session State Detection

The centerpiece. looper must know, from outside a live `claude` session, whether
it is working, needs the human, or is done — so the dashboard can reflect it and
the human only steps in when needed.

### Mechanism: hooks + PTY + sentinels

- The session runs as a native interactive `claude` process in a **PTY owned by
  the daemon** (§6). The human can attach to it for the real, native experience.
- looper injects a **scoped hook configuration** into the session's settings so
  Claude Code hooks write JSON events to a **per-session event pipe** (Unix
  socket) that the daemon watches. No user setup required.
- State is derived from hook events:

| Dashboard state | Signal |
|-----------------|--------|
| `⚙ working` | `PreToolUse` / `PostToolUse` fired (tools in flight) |
| `⏸ needs human` | `Notification` (permission_prompt), **or** sentinel `NEEDS_INPUT` |
| `✔ awaiting approval` | sentinel `DONE` present in `Stop` hook's `last_assistant_message` |

### The sentinel convention (why it's reliable)

Claude Code fires **no hook** when the `AskUserQuestion` tool is used, so
"asked a question" and "finished" are otherwise indistinguishable from outside
(confirmed limitation). looper resolves this by **controlling the step prompt**:
each interactive step's prompt instructs Claude to end its turn with a sentinel —
`@@LOOPER:NEEDS_INPUT@@` when it needs the human, `@@LOOPER:DONE@@` when the step
is complete. The `Stop` hook payload includes `last_assistant_message`; the hook
extracts the sentinel and reports the exact state.

- **Timing**: the sentinel/`Stop` evaluation runs **once per turn-boundary** (the
  pause), not per message/token. `PreToolUse`/`PostToolUse` keep the "working"
  indicator live during a turn.
- **Graceful degradation**: no sentinel present → generic "awaiting input — attach
  and check." A PTY-quiet heuristic backstops a missed `Stop`.
- The human always has final say at a pause: **approve / retry / abort**.

## 6. Client / Daemon Architecture

looper is two binaries (or one binary, two modes) communicating over a **Unix
domain socket** at `$XDG_RUNTIME_DIR/looper.sock`.

### `looperd` — daemon (owns all stateful, long-lived work)

- worker pool + step execution
- harness PTY sessions and their per-session hook event-pipes
- authoritative per-worker state (derived from hook events)
- task-claim tracking, run dirs, persistence, digests
- scheduler component (cron-style triggers) — **present in architecture, manual
  triggering only in v1**

### `looper` — client / TUI (thin, disposable)

- connects to the daemon over the socket; renders fleet/focus views (§7)
- sends commands: start / pause / stop / scale a loop; approve / retry / abort a
  step; attach to a worker
- subscribes to a **state stream** for live updates
- closing the client does not affect the daemon; reopening resyncs to live state

### Lifecycle

- **Auto-spawn (tmux-style)**: the client starts `looperd` on first command if it
  isn't already running; the daemon persists across client sessions.
- `looper daemon` runs it in the foreground for debugging.
- (v2) optional systemd user unit for always-on scheduling.

### PTY attach across the socket

The daemon holds each worker's PTY. On attach, the client proxies raw terminal
I/O (and resize events) over the socket to that PTY; `Ctrl-b d` detaches back to
the fleet view. This is the tmux server/client model. It is the most intricate
part of the build.

### Transport

**Recommended: gRPC over the Unix socket** — structured RPC for commands, server
streaming for the state feed, and a bidirectional stream for PTY I/O + resize.
Well-trodden and gives clean streaming semantics. The transport is an internal
boundary and can be swapped; a hand-rolled framed JSON+bytes protocol is an
acceptable lighter-weight alternative if gRPC tooling proves heavy.

## 7. TUI (Client)

Built with `charmbracelet/bubbletea` + `lipgloss` + `bubbles`. Two levels.

### Fleet view (top level, when concurrency > 1)

```
┌ looper · dev-loop · 3/4 workers · 5 done · 1 NEEDS YOU ┐
│    WORKER  TASK               STEP        STATE         │
│  ▸ w1      #142 fix login     implement   ⚙ working     │
│    w2      #145 add metrics   plan        ⏸ NEEDS YOU   │
│    w3      #150 refactor db   test        ⚙ working     │
│    w4      —  (idle, no task)                           │
└─────────────────────────────────────────────────────────┘
 [enter] focus  [a]ttach  [+/-] scale  [p]ause  [q]uit
```

- Each worker row is keyed by its **work unit** (`TASK_ID` + title) — that is how
  the user tells workers apart.
- Workers needing the human sort to the top and drive the header badge.

### Focus view (single worker; also the default when concurrency = 1)

```
┌ looper · dev-loop · w2 · #145 add metrics · 00:12:41 ┐
│ STEPS                    │ DETAIL                     │
│  ✔ get-task              │ plan · claude (interactive)│
│  ▶ plan     ⏸ NEEDS HUMAN│ state: ⏸ needs human       │
│    implement             │ Claude asked:              │
│    test                  │  "Should I use X or Y?"    │
│    pr                    │ ─────────────────────────  │
│                          │ [a]ttach [enter]approve    │
│ CONTEXT                  │ [r]etry  [s]kip  [x]abort  │
│  TASK_ID=145             │                            │
│  PLAN_PATH=.looper/...   │                            │
└──────────────────────────┴────────────────────────────┘
 [esc] fleet  [tab] switch pane  [a]ttach  [q]uit (resumable)
```

- `a` attaches to the focused worker's live PTY (native session); `Ctrl-b d`
  detaches. `esc` returns to the fleet.

### Guided loop builder (v1)

An in-TUI wizard to create/edit loops: name the loop, add steps, pick each step's
type and harness, fill the prompt/command, declare `outputs` and policies. Writes
the loop YAML (§8) and validates it. YAML files remain hand-editable; the builder
and raw editing are equivalent paths to the same schema.

## 8. Configuration & Storage

YAML throughout.

### Global user config — `~/.config/looper/config.yaml`

```yaml
default_harness: claude
harnesses:
  claude:
    interactive: ["claude"]
    headless:    ["claude", "-p", "{{PROMPT}}"]
    # looper auto-injects its hook config (Stop/PreToolUse/Notification → pipe)
    # into a scoped settings.json for each session. No user setup.
    sentinels:
      needs_input: "@@LOOPER:NEEDS_INPUT@@"
      done:        "@@LOOPER:DONE@@"
      no_work:     "@@LOOPER:NO_WORK@@"
```

### Project config — `.looper/` in the repo

- `.looper/loops/<name>.yaml` — one file per loop.
- `.looper/runs/<loop>/<iteration-id>/` — per-iteration record (see §9).

### Loop definition

```yaml
name: dev-loop
concurrency: 1
max_concurrency: 4
max_iterations: 0          # 0 = unbounded
workspace: shared          # shared (default) | worktree  (convenience only)
steps:
  - name: get-task
    type: script
    run: "gh issue list --json number,title --limit 1 ..."
    outputs: [TASK_ID, TASK_TITLE]
    signals_no_work: true

  - name: plan
    type: interactive
    harness: claude
    prompt: |
      Plan work for task {{TASK_ID}}: {{TASK_TITLE}}.
      End with {{SENTINEL_NEEDS_INPUT}} if you need me, else {{SENTINEL_DONE}}.
    outputs: [PLAN_PATH]

  - name: implement
    type: interactive
    prompt: "Implement the plan at {{PLAN_PATH}}. ... {{SENTINEL_DONE}}"

  - name: test
    type: script
    run: "go test ./..."
    on_fail: retry

  - name: pr
    type: script
    run: "gh pr create --fill"
```

## 9. Run Context, Workdir & Persistence

Two distinct directories per iteration — this separation is deliberate.

### looper's run dir (always looper-managed, consistent, isolation-independent)

`.looper/runs/<loop>/<iteration-id>/`:

- `context.json` — the KV vars for the iteration
- `artifacts/` — files steps produce (plan.md, diffs, **digests**)
- `steps/` — per-step logs, exit codes, timing
- `events.jsonl` — hook events / state transitions
- `digest.md` — the iteration rollup/summary
- session transcript pointers

looper **always** owns and writes this record, regardless of what the task does
or whether a worktree exists. It is the single predictable place iteration
history, digests, and state live — which is what makes `looper resume` and
inspection work uniformly.

### Execution workdir (`WORKDIR` context var — the maker's concern)

*Where steps actually run.* Defaults to the repo root (shared). Isolation is the
**loop maker's responsibility**, not something looper imposes — a discovery or
read-only ticket needs no worktree at all. The maker either:

- sets `workspace: worktree` (built-in convenience: looper creates a worktree per
  worker on task pickup, sets `WORKDIR` to it, tears it down on clean finish), or
- handles it themselves with a `script` setup step (`git worktree add …`, capture
  the path into `WORKDIR`) plus a teardown step.

Running `concurrency > 1` with `workspace: shared` is allowed; the maker owns the
consequences (fine for read-only discovery loops).

### Context flow between steps

- Steps declare `outputs: [VAR, …]`; captured values enter the run context.
- Context vars inject as **env vars** into `script`/`headless` steps and
  interpolate (`{{VAR}}`) into harness prompts. Sentinels are exposed as
  `{{SENTINEL_NEEDS_INPUT}}` etc.

### Resume

Every iteration has a complete record under `.looper/runs/…`.

- **Client reconnect** (common case): the daemon keeps running; reopening the
  client resyncs to live state, including attached PTYs. No work is lost.
- **Daemon restart**: live PTY sessions die with the daemon (their child
  processes cannot outlive it), so an interrupted `interactive` step cannot be
  rehydrated mid-turn. `looper resume` instead replays the persisted context and
  **restarts the interrupted step** from the beginning, preserving all completed
  steps and captured context. `script`/`headless`/`manual` steps resume the same
  way (re-run the interrupted step).

## 10. Component Architecture (Go packages)

Isolated, independently testable units:

- `config` — load/validate global + loop YAML
- `harness` — invocation modes + hook-config injection per harness
- `runner` — worker lifecycle, step execution, outcome resolution
- `context` — run KV, artifacts, digests, persistence, resume
- `events` — hook event-pipe → typed state events
- `pty` — session PTY management + attach/detach proxying
- `daemon` — worker pool, scheduler, socket server, authoritative state
- `rpc` — client/daemon protocol (commands, state stream, PTY stream)
- `tui` — fleet view, focus view, guided builder
- `cli` — command entrypoints (`looper`, `looper daemon`, `looper resume`, …)

## 11. Scope

### v1

- Daemon + client over Unix socket; auto-spawn lifecycle
- All four step types
- Interactive state detection (hooks + PTY + sentinels)
- PTY attach/detach across the socket
- Worker pool + concurrency + fleet/focus TUI
- Run context, run dirs, digests, resume
- `claude` harness (built-in)
- Guided loop builder + hand-editable YAML
- Manual loop triggering (start/stop/pause/scale from client)

### Designed-for, later

- Scheduled loop execution (cron-style; scheduler component already present)
- Additional harnesses (config-only, no code change expected)
- systemd user unit for always-on daemon
- `workspace: clone` isolation strategy

## 12. Build Order (guidance for the implementation plan)

1. `config` + loop schema + validation
2. `context` + run dirs + persistence
3. `runner` with `script` + `manual` steps only (no harness yet); prove the
   worker loop, outcome resolution, and termination single-worker in-process
4. `harness` + `headless` steps + hook-config injection
5. `events` pipe + interactive state detection + sentinels (still in-process TUI)
6. `pty` management + local attach/detach
7. Split into `daemon` + `rpc` + thin client; move PTY attach across the socket
8. Worker pool + concurrency + fleet view
9. Guided builder
10. Harden: resume, edge cases, degradation paths
```

