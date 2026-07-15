# looper Milestone 3 — Interactive Sessions, Hook Events & Sentinels

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. Strict TDD.

**Goal:** Run an `interactive` harness session (hand-off: child inherits the terminal), while looper listens on a per-session Unix socket that Claude Code hooks write to, derives session state (working / needs-human / awaiting-approval / no-work) from those hook events + sentinels, records it, and resolves the step outcome (human has final say via the Prompter).

**Architecture:** New `events` package (Unix-socket listener + pure state-derivation). New hidden `looper hook` subcommand that forwards a hook's stdin JSON to the session socket. `harness` gains hook-settings injection + interactive command building. New `runner.InteractiveExecutor` coordinates listener + child process + outcome. `interactive` steps become supported in the worker. No PTY multiplexing yet (that's Milestone 4) — the child inherits stdio.

**Testability (critical):** Nothing here requires a real `claude` binary. The `events` listener is tested by dialing its socket directly from Go. The `InteractiveExecutor` takes an injectable `run` func; tests substitute a fake that dials the socket, emits a scripted hook sequence, and returns — exercising the full listener→tracker→outcome path. Real `claude` wiring (`--settings`, inherited stdio) lives only in the default `run` impl, which is thin.

**Tech Stack:** Go 1.26, stdlib `net`, `encoding/json`. No new deps.

## Global Constraints

- Module `github.com/jbofill10/looper`; Go 1.26. All changes via PR into `main`.
- Real `claude` invocation (default `run` impl only): `claude --settings <scoped-settings.json> "<interpolated-prompt>"`, child inheriting os.Stdin/Stdout/Stderr. Verified flags: `--settings <file-or-json>`, prompt as positional arg.
- Injected settings hooks all run `<abs-looper-bin> hook --socket <socketPath>`. Hook events wired: `PreToolUse`, `PostToolUse`, `Notification`, `Stop`.
- Claude Code settings hooks JSON shape:
  ```json
  {"hooks":{"Stop":[{"hooks":[{"type":"command","command":"CMD"}]}],
            "PreToolUse":[{"hooks":[{"type":"command","command":"CMD"}]}],
            "PostToolUse":[{"hooks":[{"type":"command","command":"CMD"}]}],
            "Notification":[{"hooks":[{"type":"command","command":"CMD"}]}]}}
  ```
- Sentinels come from the resolved harness (`SENTINEL_*`); detected inside the `Stop` hook's `last_assistant_message`.
- Also pass `LOOPER_HOOK_SOCKET=<socketPath>` in the child env (lets test stubs find the socket; real claude ignores it).

---

### Task 1: `events` — hook types + state derivation (pure)

**Files:** Create `events/state.go`; Test `events/state_test.go`.

**Interfaces produced:**
- `events.Hook` struct (JSON tags from Claude Code hook payloads):
  ```go
  type Hook struct {
      EventName            string `json:"hook_event_name"`
      NotificationType     string `json:"notification_type"`
      ToolName             string `json:"tool_name"`
      LastAssistantMessage string `json:"last_assistant_message"`
  }
  ```
- `events.State` (string) consts: `StateStarting="starting"`, `StateWorking="working"`, `StateNeedsHuman="needs_human"`, `StateAwaitingApproval="awaiting_approval"`, `StateNoWork="no_work"`, `StateAwaitingInput="awaiting_input"`.
- `func events.Derive(prev State, h Hook, s config.Sentinels) State` — pure transition:
  - `PreToolUse`/`PostToolUse` → `StateWorking`.
  - `Notification` with `NotificationType=="permission_prompt"` → `StateNeedsHuman`.
  - `Stop`: inspect `LastAssistantMessage` — contains `s.NoWork` → `StateNoWork`; contains `s.NeedsInput` → `StateNeedsHuman`; contains `s.Done` → `StateAwaitingApproval`; none → `StateAwaitingInput`.
  - any other event → `prev` unchanged.

**Test cases (table):** each event→expected state, including all three sentinel branches, no-sentinel Stop, permission_prompt, PreToolUse, and an unknown event preserving prev.

- [ ] Step 1: write `events/state_test.go`. Step 2: FAIL. Step 3: implement `events/state.go`. Step 4: PASS. Step 5: commit `feat(events): hook types and state derivation`.

---

### Task 2: `events` — Unix socket listener

**Files:** Create `events/listener.go`; Test `events/listener_test.go`.

**Interfaces produced:**
- `events.Listener` with unexported fields.
- `func events.Listen(socketPath string) (*Listener, error)` — `net.Listen("unix", socketPath)`; spawn an accept goroutine. For each accepted conn: read all bytes (`io.ReadAll`), `json.Unmarshal` into `Hook`, send on an internal channel; close conn. Accept-loop exits cleanly when the listener is closed.
- `func (l *Listener) Events() <-chan Hook` — receive-only channel; closed when the listener is closed and the accept loop has drained.
- `func (l *Listener) Path() string`
- `func (l *Listener) Close() error` — close the net listener (unblocks Accept), wait for the accept goroutine to finish, close the events channel, `os.Remove` the socket file. Idempotent.

**Implementation notes:** guard against send-on-closed-channel — the accept goroutine owns the channel and closes it exactly once after Accept returns an error (post-Close). Use a `sync.Once` / done signaling. Choose short socket paths (unix socket paths are length-limited ~104 chars); tests should use `t.TempDir()` but if too long, fall back to a path under `os.TempDir()` with a short random-ish name derived from the test name (do NOT use randomness that breaks determinism — a counter or the test name is fine).

**Test cases:**
- Listen, dial the socket with `net.Dial("unix", path)`, write one Hook JSON, close conn → the Hook appears on `Events()` with correct fields.
- Two sequential connections → two events in order.
- After `Close()`: socket file removed; `Events()` channel is closed (ranging over it terminates).
- `Close()` twice does not panic.

- [ ] Step 1: write test. Step 2: FAIL. Step 3: implement. Step 4: PASS (run with `-race`: `go test -race ./events/...`). Step 5: commit `feat(events): unix socket hook listener`.

---

### Task 3: `looper hook` subcommand

**Files:** Create `cli/hook.go`; Test `cli/hook_test.go`.

**Interfaces produced:**
- `func cli.forwardHook(in io.Reader, socketPath string) error` — read all of `in`, `net.Dial("unix", socketPath)`, write the bytes, close. Testable core.
- `newHookCmd() *cobra.Command` — hidden (`Hidden: true`) command `hook` with required `--socket` flag; `RunE` calls `forwardHook(cmd.InOrStdin(), socket)`. Registered in `newRootCmd`.

**Test cases:**
- Start an `events.Listen` in the test, call `forwardHook(strings.NewReader(hookJSON), path)`, assert the Hook arrives on the listener's channel with expected fields.
- `forwardHook` to a nonexistent socket returns an error.

- [ ] Step 1: write test. Step 2: FAIL. Step 3: implement + register. Step 4: PASS. Step 5: commit `feat(cli): hidden hook-forwarding subcommand`.

---

### Task 4: `harness` — hook-settings injection + interactive command

**Files:** Create `harness/inject.go`; Test `harness/inject_test.go`.

**Interfaces produced:**
- `func harness.WriteHookSettings(path, looperBin, socketPath string) error` — marshal the settings JSON (shape in Global Constraints) with `command = fmt.Sprintf("%q hook --socket %q", looperBin, socketPath)` for each of the four events; write to `path` (0o644).
- `func harness.BuildInteractive(h config.Harness, prompt, settingsPath string) ([]string, error)` — returns `append(copy(h.Interactive), "--settings", settingsPath, prompt)`; error if `h.Interactive` empty.

**Test cases:**
- `WriteHookSettings` → read the file back, `json.Unmarshal` into a matching struct, assert all four event keys present and each command contains `hook --socket` and the socket path.
- `BuildInteractive(claude, "do it", "/tmp/s.json")` == `["claude","--settings","/tmp/s.json","do it"]`.
- empty Interactive → error.

- [ ] Step 1: test. Step 2: FAIL. Step 3: implement. Step 4: PASS. Step 5: commit `feat(harness): hook-settings injection and interactive command`.

---

### Task 5: extend `Prompter` for interactive confirmation

**Files:** Modify `runner/runner.go` (interface + FakePrompter), `runner/prompt.go` (StdinPrompter), existing prompter tests.

**Interface change:**
- Add to `Prompter`: `Interactive(step config.Step, finalState string) (Outcome, error)`.
- `FakePrompter` gains `InteractiveOutcome Outcome` and `InteractiveCalls int`; method returns it.
- `StdinPrompter.Interactive` prints e.g. `Session %q ended (state: %s). [a]dvance / [r]etry / [x]abort: ` and reuses `readChoice()`.

**Test cases:** `StdinPrompter.Interactive` with piped `a`/`r`/`x` → advance/retry/abort. FakePrompter returns configured outcome + counts calls.

- [ ] Step 1: extend tests. Step 2: FAIL (interface not satisfied). Step 3: implement. Step 4: `go test ./runner/...` PASS. Step 5: commit `feat(runner): prompter interactive confirmation`.

---

### Task 6: `runner.InteractiveExecutor`

**Files:** Create `runner/interactive.go`; Test `runner/interactive_test.go`.

**Interfaces produced:**
- ```go
  type InteractiveExecutor struct {
      Harness   config.Harness
      Prompter  Prompter
      LooperBin string
      // run starts the session and blocks until it exits. Injectable for tests.
      // socketPath is the listener socket; env includes LOOPER_HOOK_SOCKET.
      run func(argv, env []string, socketPath string) error
  }
  ```
- `Run(rc, step)` behavior:
  1. Compute `socketPath` under `rc.StepsDir()` (short name, e.g. `filepath.Join(rc.StepsDir(), step.Name+".sock")`; if that risks exceeding the unix limit, use `os.TempDir()` + a short name — keep deterministic).
  2. `l, err := events.Listen(socketPath)`; defer `l.Close()`.
  3. Start a consumer goroutine: `state := StateStarting`; for `h := range l.Events() { state = events.Derive(state, h, e.Harness.Sentinels); rc.AppendEvent(runctx.Event{Step:step.Name, Kind:"state", Message:string(state)}) }`; on channel close, send final `state` to a done chan.
  4. Build prompt via `harness.Interpolate` (+ `SentinelVars`), settings file via `harness.WriteHookSettings` (path under `rc.StepsDir()`), argv via `harness.BuildInteractive`.
  5. `env := append(os.Environ(), rc.Env()...)` plus `LOOPER_HOOK_SOCKET=socketPath`.
  6. Call `e.run(argv, env, socketPath)` (blocks). If `run==nil`, use the default `execInteractive` (below).
  7. Close the listener; receive `finalState` from the done chan.
  8. Outcome: `finalState==StateNoWork` → `OutcomeNoWork`. Else → `e.Prompter.Interactive(step, string(finalState))`. Capture declared `outputs` from `LOOPER_OUTPUT` if the step set any (set `LOOPER_OUTPUT` env like the other executors so an interactive session can emit outputs too).
- `func execInteractive(argv, env []string, socketPath string) error` — `cmd := exec.Command(argv[0], argv[1:]...)`; `cmd.Stdin=os.Stdin; cmd.Stdout=os.Stdout; cmd.Stderr=os.Stderr; cmd.Env=env`; `cmd.Dir` from the `WORKDIR` env entry if present; `return cmd.Run()`. (Not unit-tested; thin.)

**Test cases (inject fake `run`, no claude):**
- DONE path: fake `run` dials `socketPath`, sends a `PreToolUse` hook then a `Stop` hook whose `last_assistant_message` contains the harness Done sentinel, returns nil. Prompter is `FakePrompter{InteractiveOutcome: OutcomeAdvance}`. Assert: outcome==advance, prompter.InteractiveCalls==1, and the finalState passed was `awaiting_approval` (assert via a custom prompter capturing the arg, or extend FakePrompter to record `LastInteractiveState`).
- NO_WORK path: fake `run` sends a `Stop` with the NoWork sentinel; assert outcome==`OutcomeNoWork` and prompter NOT called.
- RETRY path: FakePrompter returns retry → outcome==retry.
- Outputs: fake `run` writes `KEY=VALUE` to `$LOOPER_OUTPUT` (it receives env? — the fake gets `env`; have it honor LOOPER_OUTPUT by writing the file) with step.Outputs declared; assert captured. (If wiring the fake to env is awkward, cover output capture via the existing script/headless tests and SKIP here — note the skip.)

Add `FakePrompter.LastInteractiveState string` set in `Interactive`.

- [ ] Step 1: test. Step 2: FAIL. Step 3: implement. Step 4: `go test -race ./runner/...` PASS. Step 5: commit `feat(runner): interactive session executor with hook-driven state`.

---

### Task 7: wire interactive into the worker

**Files:** Modify `runner/worker.go`, `runner/worker_test.go`.

**Changes:**
- `Worker` gains `LooperBin string` (absolute path to the looper binary; the CLI sets it via `os.Executable()`).
- `executorFor(StepInteractive)`: resolve harness (step.Harness/default) from `w.Global` (default `config.DefaultGlobal()` if nil); return `&InteractiveExecutor{Harness:h, Prompter:w.Prompter, LooperBin:w.LooperBin}`. Remove the "not supported" rejection for interactive; keep it only for any genuinely-unknown type (there is none left, so the default branch handles unknown strings).
- The worker must still error clearly on an unknown step type string.

**Test cases:**
- A loop with one `interactive` step, worker configured with an injected `InteractiveExecutor` whose `run` is faked — actually the worker constructs the executor internally, so instead: test at the executor level (Task 6) and here test that `executorFor` returns an `*InteractiveExecutor` for interactive steps (type-assert) and a non-nil executor with the resolved harness. Keep the worker test light: assert `executorFor` no longer errors for interactive and returns the right concrete type.

- [ ] Step 1: test. Step 2: FAIL. Step 3: implement. Step 4: `go test ./...` PASS. Step 5: commit `feat(runner): support interactive steps in the worker`.

---

### Task 8: CLI sets LooperBin + smoke

**Files:** Modify `cli/run.go`, `cli/run_test.go`.

**Changes:** `RunLoop` sets `worker.LooperBin` from `os.Executable()` (fallback to `"looper"` on error). Add `.looper/loops/plan-example.yaml` with an `interactive` plan step (documented as needing `claude`).

**Test cases:** regression — a script-only loop still runs via `RunLoop`; assert `os.Executable()` wiring doesn't break the path (no need to run claude).

- [ ] Step 1: test. Step 2: FAIL/PASS as appropriate. Step 3: implement. Step 4: `go build ./... && go test -race ./...` PASS. Step 5: commit `feat(cli): wire looper binary path for interactive hooks`.

---

## Self-Review

- Hook types + state derivation → Task 1. Socket listener → Task 2. Hook forwarder → Task 3. Settings injection + interactive argv → Task 4. Prompter extension → Task 5. Interactive executor (listener+child+outcome) → Task 6. Worker wiring → Task 7. CLI → Task 8.
- Everything tested without real `claude` via injectable `run` + direct socket dialing. Race detector on concurrent pieces (Tasks 2, 6, 8).
- PTY/attach deliberately absent (Milestone 4). Daemon absent (Milestone 5).
