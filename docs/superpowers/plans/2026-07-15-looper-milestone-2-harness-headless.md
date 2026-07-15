# looper Milestone 2 — Harness Abstraction + Headless Steps

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans or subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Add the harness abstraction, global config loading, prompt interpolation, and a working `headless` step executor so loops can run non-interactive `claude -p` (or any configured harness) steps.

**Architecture:** New `config.Global`/`config.Harness` types (global `~/.config/looper/config.yaml` with built-in `claude` defaults). New `harness` package for prompt interpolation and headless command building. New `runner.HeadlessExecutor` wired into the worker. `interactive` remains rejected until Milestone 3. Everything stays testable without a real `claude` binary by making the harness command fully configurable (tests inject a stub command).

**Tech Stack:** Go 1.26, existing deps. No new dependencies.

## Global Constraints

- Module path `github.com/jbofill10/looper`; Go 1.26.
- All changes via PR into `main` (see repo `CLAUDE.md`).
- Harness commands are argv templates. `{{PROMPT}}` in the argv is replaced with the interpolated prompt. Prompt text supports `{{VAR}}` (run-context vars) and `{{SENTINEL_NEEDS_INPUT}}` / `{{SENTINEL_DONE}}` / `{{SENTINEL_NO_WORK}}`.
- Headless output capture reuses the `LOOPER_OUTPUT` env-file convention from Milestone 1 (the executor sets `LOOPER_OUTPUT`; declared `outputs` are read from that file).
- Built-in default harness `claude`: interactive `["claude"]`, headless `["claude","-p","{{PROMPT}}"]`, sentinels `@@LOOPER:NEEDS_INPUT@@` / `@@LOOPER:DONE@@` / `@@LOOPER:NO_WORK@@`.
- TDD: test-first, stdlib `testing`, no assertion library.

---

### Task 1: Global config (`config.Global`, `config.Harness`, `config.Sentinels`)

**Files:** Create `config/global.go`; Test `config/global_test.go`.

**Interfaces produced:**
- `config.Sentinels{ NeedsInput, Done, NoWork string }` (yaml `needs_input`/`done`/`no_work`).
- `config.Harness{ Interactive []string; Headless []string; Sentinels Sentinels }` (yaml `interactive`/`headless`/`sentinels`).
- `config.Global{ DefaultHarness string; Harnesses map[string]Harness }` (yaml `default_harness`/`harnesses`).
- `func config.DefaultGlobal() *Global` — returns the built-in `claude` harness config.
- `func config.LoadGlobal(path string) (*Global, error)` — if the file does not exist, return `DefaultGlobal()`. If it exists, parse it; any harness the user omits still falls back to built-in `claude` (merge defaults so `claude` always resolves). Empty `default_harness` defaults to `"claude"`.
- `func (g *Global) ResolveHarness(name string) (Harness, error)` — `name==""` uses `g.DefaultHarness`; error if the named harness is unknown.

**Test cases (write these):**
- `LoadGlobal` on a nonexistent path returns defaults with `claude` present and `DefaultHarness=="claude"`.
- `DefaultGlobal().ResolveHarness("")` returns the claude harness (headless argv `["claude","-p","{{PROMPT}}"]`).
- `LoadGlobal` on a file defining a custom harness `foo` still resolves `claude` (merged default) AND `foo`.
- `ResolveHarness("nope")` errors.

- [ ] Step 1: Write `config/global_test.go` (cases above).
- [ ] Step 2: `go test ./config/...` → FAIL (undefined).
- [ ] Step 3: Implement `config/global.go`.
- [ ] Step 4: `go test ./config/...` → PASS.
- [ ] Step 5: Commit `feat(config): global config with built-in claude harness`.

---

### Task 2: `harness` package (interpolation + headless command building)

**Files:** Create `harness/harness.go`; Test `harness/harness_test.go`.

**Interfaces produced:**
- `func harness.Interpolate(s string, vars map[string]string) string` — replaces every `{{KEY}}` with `vars[KEY]`; unknown keys are left as-is (literal `{{KEY}}`).
- `func harness.SentinelVars(h config.Harness) map[string]string` — returns `{"SENTINEL_NEEDS_INPUT":h.Sentinels.NeedsInput, "SENTINEL_DONE":h.Sentinels.Done, "SENTINEL_NO_WORK":h.Sentinels.NoWork}`.
- `func harness.BuildHeadless(h config.Harness, prompt string) ([]string, error)` — returns a copy of `h.Headless` with every `{{PROMPT}}` token replaced by `prompt`; error if `h.Headless` is empty.

**Test cases:**
- `Interpolate("plan {{TASK_ID}} end {{SENTINEL_DONE}}", {"TASK_ID":"42","SENTINEL_DONE":"@@D@@"})` == `"plan 42 end @@D@@"`.
- `Interpolate` leaves `{{UNKNOWN}}` untouched.
- `BuildHeadless(claude, "hi")` == `["claude","-p","hi"]`.
- `BuildHeadless` on a harness with empty `Headless` errors.

- [ ] Step 1: Write `harness/harness_test.go`.
- [ ] Step 2: `go test ./harness/...` → FAIL.
- [ ] Step 3: Implement `harness/harness.go`.
- [ ] Step 4: `go test ./harness/...` → PASS.
- [ ] Step 5: Commit `feat(harness): prompt interpolation and headless command building`.

---

### Task 3: `runner.HeadlessExecutor`

**Files:** Create `runner/headless.go`; Test `runner/headless_test.go`.

**Interfaces produced:**
- `runner.HeadlessExecutor{ Harness config.Harness; Prompter Prompter }` implementing `Executor`.
- Behavior of `Run(rc, step)`:
  1. Build `vars` = copy of `rc.Vars` merged with `harness.SentinelVars(e.Harness)`.
  2. `prompt = harness.Interpolate(step.Prompt, vars)`.
  3. `argv, err = harness.BuildHeadless(e.Harness, prompt)`.
  4. Run `argv[0]` with `argv[1:]` via `exec.Command`, `Dir = WORKDIR`, env = `os.Environ()+rc.Env()+LOOPER_OUTPUT=<steps/<name>.outputs>`, stdout+stderr → `steps/<name>.log`.
  5. Capture declared `step.Outputs` from the `LOOPER_OUTPUT` file (reuse the same capture logic as `ScriptExecutor` — extract the Milestone 1 `captureOutputs` helper if not already shared; it already lives in `runner/script.go` and is package-visible, so call it directly).
  6. Outcome: exit 0 → `OutcomeAdvance`; if `step.SignalsNoWork && exit==NoWorkExitCode` → `OutcomeNoWork`; else resolve via `on_fail` (reuse the same failure-resolution logic as script — extract a shared `resolveFailure(prompter, step, exitCode)` free function used by both executors).

**Refactor note:** In `runner/script.go`, change `func (e *ScriptExecutor) resolveFailure(...)` into a free function `func resolveFailure(p Prompter, step config.Step, exitCode int) (Outcome, error)` and have both `ScriptExecutor` and `HeadlessExecutor` call it. Update `ScriptExecutor.Run` accordingly. Keep `captureOutputs` as the shared package-level helper.

**Test cases (use a stub harness — no real claude):**
- Success: harness headless `["sh","-c","echo done"]`; expect `OutcomeAdvance`.
- Interpolation + outputs: harness headless `["sh","-c","{{PROMPT}}"]`, step prompt `echo TASK_ID=7 >> "$LOOPER_OUTPUT"`, outputs `[TASK_ID]`; after run `rc.Get("TASK_ID")=="7"`.
- Sentinel var available: prompt `echo NEEDS={{SENTINEL_DONE}} >> "$LOOPER_OUTPUT"` with outputs `[NEEDS]`, claude sentinels default; `rc.Get("NEEDS")` == the done sentinel string.
- Failure + on_fail=abort: headless `["sh","-c","exit 1"]`, `OnFailAbort` → `OutcomeAbort`.
- No-work: headless `["sh","-c","exit 78"]`, `SignalsNoWork=true` → `OutcomeNoWork`.

- [ ] Step 1: Write `runner/headless_test.go`.
- [ ] Step 2: `go test ./runner/...` → FAIL.
- [ ] Step 3: Refactor `runner/script.go` (shared `resolveFailure`) and implement `runner/headless.go`.
- [ ] Step 4: `go test ./runner/...` → PASS (existing script/worker tests still green).
- [ ] Step 5: Commit `feat(runner): headless step executor`.

---

### Task 4: Wire harness into the worker + CLI

**Files:** Modify `runner/worker.go`, `runner/worker_test.go`, `cli/run.go`, `cli/run_test.go`.

**Interfaces produced/changed:**
- `runner.Worker` gains field `Global *config.Global` and `HarnessName string` (default resolution: per-step `step.Harness` overrides; else `Global.DefaultHarness`).
- `Worker.executorFor(step)`:
  - `StepScript` → `&ScriptExecutor{Prompter: w.Prompter}`
  - `StepManual` → `&ManualExecutor{Prompter: w.Prompter}`
  - `StepHeadless` → resolve harness (`step.Harness` or default) from `w.Global`; return `&HeadlessExecutor{Harness: h, Prompter: w.Prompter}`. If `w.Global==nil`, use `config.DefaultGlobal()`.
  - `StepInteractive` → still error `not supported until a later milestone`.
- `cli.RunOptions` unchanged externally, but `RunLoop` now loads global config via `config.LoadGlobal(globalPath())` where `globalPath()` returns `$XDG_CONFIG_HOME/looper/config.yaml` or `~/.config/looper/config.yaml`, and passes it to the worker.

**Test cases:**
- Worker test: a loop with a `headless` step using a stub harness injected via `Global` runs to completion (max_iterations:1) and the step's outcome is advance. (Inject a `config.Global` whose default harness headless is `["sh","-c","{{PROMPT}}"]`.)
- Worker still rejects `interactive`.
- CLI test: `RunLoop` on a loop dir with a headless step (stub harness via a temp global config file passed through — OR verify default path resolution doesn't crash when file missing). Keep it simple: test that `RunLoop` runs a script-only loop unchanged (regression) — headless-with-real-claude is not unit-tested at the CLI layer.

- [ ] Step 1: Update worker + CLI tests (headless via injected Global).
- [ ] Step 2: `go test ./...` → FAIL on new expectations.
- [ ] Step 3: Implement worker `Global`/`executorFor` changes and CLI global-config loading.
- [ ] Step 4: `go test ./...` → PASS.
- [ ] Step 5: Commit `feat(runner,cli): wire harness resolution into worker`.

---

### Task 5: Sample + docs

**Files:** Create `.looper/loops/headless-example.yaml`; update `CLAUDE.md` run notes if needed.

- [ ] Step 1: Add a sample loop demonstrating a headless step (guarded so it doesn't require claude to smoke-test — use a `script` first step; document that the headless step needs `claude` installed).
- [ ] Step 2: `go build ./... && go test ./...` → all pass.
- [ ] Step 3: Commit `docs: headless loop example`.

---

## Self-Review

- Global config + built-in claude harness → Task 1.
- Prompt interpolation + sentinels + headless argv build → Task 2.
- Headless executor with output capture + outcome resolution → Task 3.
- Worker/CLI wiring + harness resolution → Task 4.
- No new deps; interactive still deferred; everything testable with stub harness (no real claude needed). ✓
