# looper Milestone 5b — Loops in the Daemon + State Streaming + Decisions

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Strict TDD. Checkbox steps.

**Goal:** The daemon runs loops (`script`/`headless`/`manual`), streams live state to clients, and round-trips human decisions (manual steps, `on_fail=ask`) to the client over gRPC. Client gets `looper start`, `looper ls`, `looper stop`.

**Architecture:** New `daemon.Manager` (pure, no gRPC) owns runs: starts a `runner.Worker` per run in a goroutine, fans out `Update`s to subscribers, and services decision requests via a client round-trip. A thin gRPC layer wraps the Manager. `runner.Worker` gains reporting hooks + a run context so the daemon can observe progress and cancel. Interactive steps are rejected by the daemon Manager (they need PTY-attach across the socket — Milestone 5c); `looper run` still handles them locally. The generated `rpc` stubs for these RPCs are already committed.

**Tech Stack:** Go 1.26, `google.golang.org/grpc`. No new deps.

## Global Constraints

- Module `github.com/jbofill10/looper`; Go 1.26. PR into `main`.
- Decision options are always `["advance","retry","abort"]`; the daemon maps them to `runner.Outcome` (`advance→OutcomeAdvance`, `retry→OutcomeRetry`, `abort→OutcomeAbort`).
- The daemon Manager rejects loops containing any `interactive` step with a clear error at `StartLoop` time (deferred to M5c).
- Race-safety: `go test -race ./...` clean. Buffered subscriber channels (cap 64); slow/absent subscribers must never block the worker (drop-oldest or non-blocking send is acceptable for `log`/`state` kinds, but `decision_request` delivery must be reliable — see Task 2 notes).

---

### Task 1: `runner.Worker` reporting + cancellation hooks

**Files:** Modify `runner/worker.go`, `runner/worker_test.go`.

**Changes:**
- New type `runner.Report struct { Kind, Step, State, Message string; Iteration int }` with kind consts `ReportIteration="iteration"`, `ReportStepStart="step_start"`, `ReportOutcome="outcome"`, `ReportRunDone="run_done"`.
- `Worker` gains:
  - `OnReport func(Report)` — called (if non-nil) at: iteration start (`ReportIteration`), each step start (`ReportStepStart`), each step outcome (`ReportOutcome`, `State`=outcome string), and once when the run loop ends (`ReportRunDone`).
  - `Ctx context.Context` — if non-nil, the iteration loop checks `Ctx.Err()` before each step and between iterations; on cancellation `Run` returns `Ctx.Err()` (wrapped) promptly.
- Behaviour otherwise unchanged; `OnReport`/`Ctx` nil ⇒ identical to before.

**Test cases:** a 1-iteration script loop records the report sequence (iteration, step_start, outcome, …, run_done) via a captured slice; a cancelled `Ctx` stops the loop before running further steps.

- [ ] TDD → commit `feat(runner): worker reporting and cancellation hooks`.

---

### Task 2: `daemon.Manager` (pure loop orchestration)

**Files:** Create `daemon/manager.go`; Test `daemon/manager_test.go`.

**Interfaces produced:**
- `type Update struct { RunID, Kind, LoopName string; Iteration int; Step, State, Message, RequestID string; Options []string }`
- `type RunInfo struct { RunID, LoopName, Status string; Iteration int; CurrentStep, State, Err string }` (Status: `running|done|stopped|error`).
- `type Manager struct { ... }` with:
  - `func NewManager(global *config.Global, looperBin string) *Manager` (nil global ⇒ `config.DefaultGlobal()`).
  - `func (m *Manager) StartLoop(loopName, loopFile, baseDir, workdir string) (runID string, err error)` — load+validate loop (`config.LoadLoop`); reject interactive; assign a run id (injectable `m.newID`, default counter); build a `runner.Worker` with `OnReport` → `m.publish`, a `remotePrompter` (below), a cancellable `Ctx`; launch `go worker.Run()` and update the run's `Status` to done/error/stopped on completion (publish a `run_done` Update).
  - `func (m *Manager) StopLoop(runID string) error` — cancel the run's context; Status→stopped.
  - `func (m *Manager) ListRuns() []RunInfo` — snapshot, stable-sorted by run id.
  - `func (m *Manager) Subscribe(runID string) (<-chan Update, func())` — register a buffered (64) channel; empty runID subscribes to all runs; the unsubscribe func removes and closes it. On subscribe, synthesize a current-status `state` Update for the matching run(s) so a late subscriber sees current state.
  - `func (m *Manager) Respond(runID, requestID, outcome string) error` — deliver the outcome to the waiting `remotePrompter` (error if no such pending request).
- Unexported `remotePrompter` implements `runner.Prompter`:
  - `Manual`/`AskFailure` → register a pending request (fresh request id), publish a `decision_request` Update (Options advance/retry/abort), block until `Respond` delivers or the run ctx is cancelled (→ `OutcomeAbort`).
  - `Interactive` → return an error (not reachable; interactive rejected at StartLoop, but implement defensively returning abort+error).

**Concurrency notes:** guard `runs`, per-run `subs`, and `pending` with a mutex (do not hold it while sending on channels). `decision_request` and `run_done` must reach subscribers reliably: send with a small timeout or a dedicated unbuffered handoff is overkill — instead, use cap-64 buffered channels and, for the reliable kinds, block-send (these are low-frequency). For high-frequency kinds (none yet beyond step reports) a non-blocking send is fine. Keep it simple: block-send to each subscriber from a per-run fanout goroutine fed by an internal cap-256 channel, so the worker never blocks on subscribers.

**Test cases (Manager directly, no gRPC):**
- Script-only 1-iteration loop: Subscribe(all), StartLoop, drain updates until a `run_done`; assert Status ends `done` and the report kinds appeared.
- Manual step: Subscribe, StartLoop a `[manual]` loop, receive a `decision_request` with a request id, call `Respond(runID, reqID, "advance")`, assert `run_done` with Status done.
- `Respond` with `abort` on a manual loop → the run ends (Status done, iteration completed via abort semantics) — assert no panic and run terminates.
- `StopLoop` mid-run: a loop whose script sleeps briefly; StopLoop → Status stopped and worker returns.
- Interactive rejection: StartLoop on a loop with an interactive step → error, no run created.

- [ ] TDD → `go test -race ./daemon/...` → commit `feat(daemon): loop manager with state fanout and decisions`.

---

### Task 3: gRPC service wrapping the Manager

**Files:** Modify `daemon/daemon.go` (Server holds a `*Manager`); Create `daemon/service.go`; Test `daemon/service_test.go`.

**Changes:**
- `daemon.New()` constructs a `Manager` (looperBin via `os.Executable()` fallback `"looper"`); `Server` exposes it.
- Implement the new RPCs on `Server` (in `service.go`):
  - `StartLoop` → `Manager.StartLoop`, return run id.
  - `StopLoop` → `Manager.StopLoop`.
  - `ListRuns` → map `[]RunInfo` to `[]*rpc.RunInfo`.
  - `StreamState(req, stream)` → `Subscribe(req.RunId)`; forward `Update`→`rpc.StateUpdate` on the stream until the client disconnects (`stream.Context().Done()`) or the subscription closes; unsubscribe on return.
  - `RespondDecision` → `Manager.Respond`.

**Test cases:** one in-process gRPC integration test over a real unix socket: `StartLoop` a script loop, `StreamState`, read updates until `run_done`; and a manual-loop test that reads a `decision_request` off the stream and calls `RespondDecision` to complete it.

- [ ] TDD → `go test -race ./daemon/...` → commit `feat(daemon): grpc loop-control and state-stream service`.

---

### Task 4: CLI `start` / `ls` / `stop`

**Files:** Create `cli/loops.go`; modify `cli/root.go`; Test `cli/loops_test.go`.

**Commands:**
- `looper start [loop-name] [--file] [--socket]` — `EnsureDaemon`; `StartLoop` (base_dir = `<cwd>/.looper`, workdir = cwd); then `StreamState(run_id)` and render each update as a concise line (`iter N · step · state`). On `decision_request`, prompt on stdin (`[a]dvance/[r]etry/[x]abort`) and `RespondDecision`. Exit when `run_done`.
- `looper ls [--socket]` — `EnsureDaemon`? No — `ls` should not spawn; if daemon not running, print "no daemon running" and exit 0. Else `ListRuns` and print a table.
- `looper stop <run-id> [--socket]` — `Dial` (no spawn); `StopLoop`.
- Register in `newRootCmd`.

**Test cases:** integration smoke (built binary): start the daemon, `looper start` a script-only loop with `--socket`, assert it streams and exits 0 and a run dir was created; then `looper ls` shows the run; `looper shutdown`. Pipe `a\n` to stdin for any manual prompt (use a script-only loop to avoid needing it). `t.Skip` if `go build` unavailable.

- [ ] TDD → `go build ./... && go test -race ./cli/...` → commit `feat(cli): start, ls, and stop commands`.

---

## Self-Review

- Worker hooks → Task 1. Manager (fanout + decisions + cancel + interactive-reject) → Task 2. gRPC wrap → Task 3. CLI → Task 4.
- `go test -race ./...` clean; worker never blocks on subscribers (per-run fanout goroutine). Interactive deferred to M5c; `looper run` unaffected.
