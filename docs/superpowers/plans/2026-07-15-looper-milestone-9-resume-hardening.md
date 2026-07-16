# looper Milestone 9 — Resume & Hardening

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Strict TDD. Checkbox steps.

**Goal:** Make interrupted runs resumable (persist per-iteration step progress; continue from the interrupted step preserving completed steps + context), add `looper resume`, handle daemon termination signals gracefully, and add project docs (`README.md`, `looper version`).

**Architecture:** `runctx` persists step progress (`progress.json`). `runner.Worker` gains an optional resume of a specific iteration dir (skip completed steps, reuse persisted context) before continuing normal iterations. CLI `looper resume` finds the latest incomplete iteration for a loop and continues it locally. The daemon installs a SIGINT/SIGTERM handler that calls `Stop()` for clean shutdown + socket removal. A README documents the tool.

**Tech Stack:** Go 1.26. No new deps.

## Global Constraints

- Module `github.com/jbofill10/looper`; Go 1.26. PR into `main`. `go test -race ./...` clean.
- Progress model: an iteration is "complete" when all steps advanced through the end; "incomplete" if interrupted mid-iteration. Completed steps are those that returned `advance` (or `no-work` ends the loop). Resume re-runs from the first not-yet-completed step, reusing the persisted context vars.
- Resume never re-runs a completed step; it does re-run the interrupted step from the beginning (its side effects may repeat — documented, matches the spec).

---

### Task 1: `runctx` progress persistence

**Files:** Modify `runctx/context.go`; add to `runctx/context_test.go`.

**Interfaces produced:**
- `type Progress struct { Completed []string; Done bool }` (JSON).
- `func (rc *RunContext) SaveProgress(p Progress) error` — write `progress.json`.
- `func (rc *RunContext) LoadProgress() (Progress, error)` — read it (return zero Progress if absent, no error).

**Test cases:** save+load round-trip; load-when-absent returns empty Progress, nil error.

- [ ] TDD → commit `feat(runctx): per-iteration progress persistence`.

---

### Task 2: `runner.Worker` resume

**Files:** Modify `runner/worker.go`, `runner/worker_test.go`.

**Changes:**
- After each step that returns `advance`, append the step name to the iteration's Progress and `SaveProgress`. When the iteration finishes all steps, `SaveProgress({..., Done:true})`.
- `Worker` gains `ResumeDir string`. When set, the FIRST iteration:
  - loads context from `ResumeDir` (`runctx.Load`) instead of creating a fresh dir,
  - loads Progress, and starts the step loop at the first step whose name is NOT in `Completed` (skipping completed steps; their outcomes are treated as already-advanced),
  - reuses the same `WORKDIR` from the loaded context (or re-sets it).
  - After the resumed iteration completes, subsequent iterations are fresh (ResumeDir consumed).
- Reporting: emit a `ReportOutcome` with state `resumed-skip` for skipped steps (so observers see them), then proceed normally.

**Test cases:**
- Manually create an iteration dir with `context.json` (`TASK_ID=7`, `WORKDIR=...`) and `progress.json` `{Completed:["get-task"], Done:false}`; run a Worker with `ResumeDir` set on a 2-step loop (`get-task`, `work`); assert `get-task` is skipped (a marker it would write is absent / a "resumed-skip" report emitted) and `work` runs with `TASK_ID=7` in its env.
- A fully-`Done` progress dir ⇒ the resumed iteration runs no steps and moves on.
- Normal (no ResumeDir) behavior unchanged; progress.json now written each run (assert it exists and lists completed steps).

- [ ] TDD → `go test -race ./runner/...` → commit `feat(runner): resume an interrupted iteration`.

---

### Task 3: `looper resume` CLI

**Files:** Create `cli/resume.go`; modify `cli/root.go`; Test `cli/resume_test.go`.

**Command `looper resume [loop-name] [--file] [--socket]`:**
- Resolve the loop (like `run`); scan `<baseDir>/runs/<loop>/` (and worker subdirs) for the most recent iteration dir whose `progress.json` has `Done:false` (or missing but has a `context.json`); if none, print "nothing to resume" and exit 0.
- Build a local `runner.Worker` with `ResumeDir` = that dir and run it (reusing the `RunLoop` machinery — add an internal option `ResumeDir string` to `RunOptions`).
- Testable core: a helper `findResumeDir(baseDir, loopName string) (string, bool)` unit-tested against a synthesized runs tree.

**Test cases:** `findResumeDir` picks the latest incomplete iteration and ignores `Done:true` ones; `RunLoop` with `ResumeDir` set resumes (script loop: pre-mark `get-task` complete, assert only later steps run). Integration smoke optional.

- [ ] TDD → `go build ./... && go test -race ./...` → commit `feat(cli): resume an interrupted loop`.

---

### Task 4: daemon signal handling + `version` + README

**Files:** Modify `cli/daemon.go` (or `daemon`), `cli/root.go`; Create `README.md`; Test as noted.

**Changes:**
- The `looper daemon` foreground command installs a `signal.Notify` for SIGINT/SIGTERM in a goroutine that calls `server.Stop()`, so Ctrl-C shuts the daemon down cleanly and removes the socket. (Verify Serve returns; socket removed.)
- `looper version` prints `daemon.Version` (rename/æxpose a shared `Version` const if cleaner). Also wire `RootCmd.Version`.
- `README.md`: what looper is (loop abstraction, workers, harness/steps, daemon+TUI), install/build, quickstart (`looper new`, `looper run`, `looper start`, `looper tui`, `looper attach`, `looper resume`), the step types, loop YAML schema, and a pointer to `docs/superpowers/specs`.

**Test cases:** `version` command prints the version string (test via built binary or the command's output buffer); signal handling verified via the built-binary integration (send SIGINT to a spawned `looper daemon`, assert it exits and the socket is removed) — `t.Skip` if no `go build`.

- [ ] TDD → `go build ./... && go test -race ./...` → commit `feat(cli): daemon signal handling, version command, and README`.

---

## Self-Review

- Progress persistence → Task 1. Worker resume → Task 2. `looper resume` → Task 3. Signals + version + README → Task 4.
- Resume reuses persisted context and skips completed steps, re-running only the interrupted step onward. `go test -race` clean. This completes the v1 build order.
