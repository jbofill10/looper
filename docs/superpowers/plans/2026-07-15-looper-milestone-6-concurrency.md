# looper Milestone 6 — Worker Pool Concurrency

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Strict TDD. Checkbox steps.

**Goal:** A daemon run spawns a pool of N workers (concurrency), each running the loop independently and pulling a distinct work unit; per-worker state is tracked and streamed (the data the fleet view will render). Task-acquisition is serialized to avoid double-pulls.

**Architecture:** `runner.Worker` gains an id, a task-var, an optional shared acquisition lock, and worker-tagged reports. `daemon.Manager` StartLoop spawns N workers per run, aggregates per-worker state into `RunInfo.workers`, and tags every `Update`/decision with a `worker_id`. `RespondDecision` already routes by unique `request_id`, so it works unchanged across workers. Generated proto fields (`concurrency`, `WorkerInfo`, `StateUpdate.worker_id/task`) are committed.

**Tech Stack:** Go 1.26. No new deps.

## Global Constraints

- Module `github.com/jbofill10/looper`; Go 1.26. PR into `main`. `go test -race ./...` clean.
- Worker ids: `w1`, `w2`, … within a run. Each worker's iteration run dirs are namespaced by worker id to avoid collisions (`.looper/runs/<loop>/<runid>/<workerid>/<iter>` OR an iteration-id prefixed with the worker id under the existing layout — pick one and be consistent).
- Task identity: the value of the loop's task var (default `TASK_ID`; overridable via a loop field `task_var`). Absent ⇒ empty task string (fine).
- Acquisition serialization: steps with `signals_no_work: true` run under a per-run mutex shared by all workers of that run, so two workers never pull simultaneously. Distinct-unit correctness beyond that is the get-task script's responsibility (documented).
- A run's `Status` is `done` only when ALL workers have finished; `running` while any worker is active.

---

### Task 1: config — `task_var`

**Files:** Modify `config/loop.go`, `config/loop_test.go`.

- Add `Loop.TaskVar string` (yaml `task_var`); `Validate` defaults it to `"TASK_ID"` when empty.
- Test: parse a loop with/without `task_var`; default applied.

- [ ] TDD → commit `feat(config): task_var for identifying work units`.

---

### Task 2: `runner.Worker` — id, task, shared acquisition lock, worker-tagged reports

**Files:** Modify `runner/worker.go`, `runner/worker_test.go`.

**Changes:**
- `Report` gains `WorkerID string` and `Task string`.
- `Worker` gains: `ID string`; `TaskVar string` (default `"TASK_ID"` if empty); `AcquireLock sync.Locker` (optional).
- In the iteration loop: every `OnReport` call is tagged with `w.ID` and the current task = `rc.Get(w.TaskVar)` (looked up after the acquisition step runs).
- Around executing a step whose `SignalsNoWork` is true: if `w.AcquireLock != nil`, `Lock()`/`Unlock()` for the duration of that step only.
- Behaviour with nil AcquireLock / empty ID unchanged (existing tests pass).

**Test cases:**
- Reports carry `WorkerID` and, after an acquisition step sets `TASK_ID`, the `Task`.
- Two workers sharing an `AcquireLock` and a shared counter-file get-task (exit 78 after K pulls) never both pull the same increment — assert the set of `TASK_ID`s pulled across both workers has no duplicates. (Use a real `sync.Mutex` and a shell get-task that appends its pulled id to a file; assert uniqueness.)

- [ ] TDD → `go test -race ./runner/...` → commit `feat(runner): worker id, task tracking, and shared acquisition lock`.

---

### Task 3: `daemon.Manager` — worker pool per run

**Files:** Modify `daemon/manager.go`, `daemon/manager_test.go`.

**Changes:**
- `StartLoop` gains a `concurrency int` parameter (0 ⇒ `loop.Concurrency`). Clamp to `[1, loop.MaxConcurrency]`.
- Each run holds `workers map[string]*workerState` (worker_id → {task, iteration, step, state, status}) plus the aggregate.
- Spawn N `runner.Worker`s (ids `w1..wN`), sharing: one `AcquireLock` (a `*sync.Mutex`), the run's ctx, the same `OnReport` (which updates `run.workers[report.WorkerID]` and publishes a worker-tagged `Update`), and a `remotePrompter` that tags decision requests with the worker id. Namespace each worker's iteration dirs by id.
- The run's aggregate `Status` becomes `done` when all workers return; `error` if any returned a non-cancel error; `stopped` if cancelled.
- `ListRuns` populates `RunInfo.Workers` from `run.workers` (stable-sorted by worker id).
- `remotePrompter` must be per-worker (so its published decision_request carries that worker's id), but they share the run's `pending` map + `Respond` (keyed by request id).

**Test cases:**
- `StartLoop` a script loop with `concurrency=3` and a shared-counter get-task that yields 5 distinct tasks then no-work: assert 3 workers ran, all 5 tasks consumed exactly once (inspect run dirs or a sink file), run Status ends `done`.
- Per-worker updates carry distinct `WorkerID`s.
- A manual-step loop with concurrency=2: two decision_requests (one per worker) with different worker ids and request ids; `Respond` each → run completes.
- `ListRuns` returns per-worker info.

- [ ] TDD → `go test -race ./daemon/...` → commit `feat(daemon): worker-pool concurrency per run`.

---

### Task 4: gRPC + CLI surface for concurrency

**Files:** Modify `daemon/service.go`, `cli/loops.go`, tests.

**Changes:**
- `StartLoop` service method passes `req.Concurrency` through; `StreamState`/`ListRuns` include worker fields (already mapped if you map all fields).
- `looper start` gains `--concurrency N` flag (default 0 ⇒ loop config). Its streamed lines include the worker id (`w2 · iter 1 · plan · needs_human`). Decision prompts state which worker.
- `looper ls` prints per-worker rows under each run (or a flat worker table): `RUN  WORKER  TASK  STEP  STATE`.

**Test cases:** CLI integration smoke (built binary): `looper start --concurrency 2` on a script loop with a shared-counter get-task yielding a few tasks → streams worker-tagged lines, exits 0, run dirs for both workers exist; `looper ls` shows two workers. `t.Skip` if `go build` unavailable.

- [ ] TDD → `go build ./... && go test -race ./...` → commit `feat(cli): concurrency flag and per-worker status`.

---

## Self-Review

- task_var → Task 1. Worker id/task/lock/reports → Task 2. Manager worker pool + per-worker state + aggregate status → Task 3. gRPC/CLI surface → Task 4.
- Distinct-unit safety via shared acquisition lock; `go test -race` clean. Sets up the per-worker data the fleet-view TUI (next milestone) renders.
