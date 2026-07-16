# looper Milestone 7 — Fleet & Focus TUI

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Strict TDD. Checkbox steps.

**Goal:** A Bubble Tea client (`looper` with no subcommand, and `looper tui`) that connects to the daemon, renders a live **fleet view** (all workers across runs, keyed by task, needs-you sorted to top) and a **focus view** (a worker's step pipeline + context + detail), handles decision prompts (advance/retry/abort), and attaches to a live session.

**Architecture:** A pure, unit-testable `tui.Model` (Bubble Tea `Model`) holds aggregated run/worker state and renders both views; it never touches the network directly. A thin `tui` program wiring reads the gRPC `StreamState` into `stateUpdateMsg`s and turns key presses into commands that call injected callbacks (`onDecision`, `onAttach`). Attach suspends the Bubble Tea program, runs the M5c raw-mode attach, then resumes. All view/update logic is tested with synthetic messages; the tea.Program run + attach suspend/resume are thin and manually verified.

**Tech Stack:** Go 1.26, `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/lipgloss`. (Deps added.)

## Global Constraints

- Module `github.com/jbofill10/looper`; Go 1.26. PR into `main`. `go test -race ./...` clean.
- `Model` is pure: no gRPC calls inside `Update`/`View`. Side-effecting actions (respond to a decision, attach) are `tea.Cmd`s that invoke injected function fields (`RespondFn func(runID, reqID, outcome string) tea.Cmd`, `AttachFn func(runID string) tea.Cmd`), so tests can substitute fakes and assert the right args.
- States rendered with clear glyphs: working `⚙`, needs-human `⏸`, awaiting-approval `✔?`, done `✔`, no-work `∅`. Needs-human workers sort to the top of the fleet and drive a header badge (`N NEED YOU`).

---

### Task 1: `tui.Model` state aggregation

**Files:** Create `tui/model.go`; Test `tui/model_test.go`.

**Interfaces produced:**
- Message types: `StateUpdateMsg` (mirrors `daemon.Update` fields incl. WorkerID/Task/RequestID/Options), `RunsSnapshotMsg []RunSnapshot`, `ErrMsg{Err error}`, `DecisionSentMsg{RunID, RequestID string}`.
- `type workerRow struct { RunID, LoopName, WorkerID, Task, Step, State, Status, PendingReqID string; PendingOptions []string; Iteration int }`
- `type Model struct { ... }` implementing `tea.Model` (`Init`, `Update`, `View`).
- `func NewModel(opts Options) Model` where `Options{ RespondFn, AttachFn, Quit bool }`.
- `Update` handling of `StateUpdateMsg`: upsert the worker row keyed by (RunID, WorkerID); set/clear pending decision on `decision_request`/subsequent `state`/`run_done`; remove/mark workers on `run_done`.
- Helper (exported for tests): `func (m Model) Workers() []workerRow` returning rows sorted needs-human-first then by RunID/WorkerID; `func (m Model) NeedYouCount() int`.

**Test cases:** apply a sequence of `StateUpdateMsg`s for 2 runs × 2 workers; assert `Workers()` count, ordering (a needs-human worker first), `NeedYouCount()`; a `decision_request` sets `PendingReqID`/`PendingOptions`; a later `state` update clears it; `run_done` marks the run's workers done.

- [ ] TDD → commit `feat(tui): model state aggregation`.

---

### Task 2: fleet + focus rendering + navigation

**Files:** Modify `tui/model.go`; add `tui/view_test.go`.

**Changes:**
- `Model` has `view` (fleet|focus), `cursor` int, `focusRun/focusWorker`.
- `Update` key handling: `up/k`,`down/j` move cursor (fleet); `enter` → focus the selected worker; `esc` → back to fleet; `q`/`ctrl+c` → quit (`tea.Quit`); in focus with a pending decision, `a`/`r`/`x` → `RespondFn(...)` cmd + optimistic clear; `a` with no pending decision in focus → `AttachFn(focusRun)`.
- `View`:
  - Fleet: header (`looper · N runs · M NEED YOU`), a table of worker rows (`▸` cursor, worker id, task, step, state glyph), footer key hints.
  - Focus: title (`<worker> · <task>`), the run's step list with the current step marked and its state, a context/vars block if available, a decision prompt when pending, footer hints.

**Test cases (View returns a string):** fleet view string contains the header badge and a row per worker with the right glyph; cursor movement changes the `▸` position; `enter` switches to focus and the focus string contains the worker/task and step; pressing `a`/`r`/`x` with a pending decision invokes `RespondFn` with the right (runID, reqID, outcome) — assert via a fake RespondFn that records calls (return `nil` cmd); pressing `a` with no pending decision invokes `AttachFn(runID)`.

- [ ] TDD → `go test ./tui/...` → commit `feat(tui): fleet and focus views with navigation`.

---

### Task 3: program wiring (stream → msgs, attach suspend/resume)

**Files:** Create `tui/program.go`; Test minimal.

**Interfaces produced:**
- `func Run(ctx context.Context, cl rpc.LooperClient, conn io.Closer) error` — build the `Model` with real `RespondFn`/`AttachFn`, start a goroutine that opens `StreamState("")` and forwards updates to the program via `p.Send(StateUpdateMsg{...})`, prime it with a `ListRuns` snapshot, and `tea.NewProgram(model).Run()`.
- `AttachFn` implementation: returns a `tea.Cmd` that uses `tea.ExecProcess`-style suspension — since attach needs raw terminal control, use `tea.Exec` with a custom `tea.ExecCommand` OR release the program (`p.ReleaseTerminal()` / `p.RestoreTerminal()`) around a call into the shared attach routine from `cli/attach.go`. Extract the attach bridge from `cli/attach.go` into a reusable `func attachStream(ctx, cl, runID string, in, out *os.File) error` (in a shared place, e.g. `client` package) so both the CLI command and the TUI call it.
- `RespondFn` implementation: calls `cl.RespondDecision(...)` and returns a `DecisionSentMsg`/`ErrMsg`.

**Test note:** the tea.Program run and terminal suspend/resume are NOT unit-tested (need a TTY). Test only that `Run` wires a stream goroutine that translates a fake stream's updates into `StateUpdateMsg`s delivered to the model — if this is awkward without a running program, keep `Run` thin and instead unit-test the pure translation helper `updateFromProto(*rpc.StateUpdate) StateUpdateMsg`. Add that helper and test it.

- [ ] TDD (translation helper + attach extraction compiles & CLI still works) → `go build ./... && go test ./...` → commit `feat(tui): program wiring, stream translation, attach integration`.

---

### Task 4: CLI entrypoint

**Files:** Modify `cli/root.go`; Create `cli/tui.go`; adjust `cli/attach.go` to use the shared `attachStream`.

**Changes:**
- `looper tui [--socket]` and bare `looper` (no args) → `EnsureDaemon`, dial, `tui.Run(...)`.
- Bare `looper` with no subcommand runs the TUI (set `RootCmd.RunE` when args empty; keep subcommands working). If not a terminal (piped), print a hint and exit 0 instead of launching the TUI.
- `cli/attach.go` now delegates to the shared `attachStream`.

**Test cases:** a non-TTY invocation of the TUI entrypoint prints the hint and exits 0 (no hang) — test via built binary with piped stdin, `t.Skip` if no `go build`. Existing attach smoke still passes.

- [ ] TDD → `go build ./... && go test -race ./...` → commit `feat(cli): tui entrypoint (default command)`.

---

## Self-Review

- Model aggregation → Task 1. Views + nav + decisions/attach keys → Task 2. Program wiring + stream translation + attach extraction → Task 3. CLI entrypoint → Task 4.
- Pure model logic fully unit-tested; tea.Program/attach terminal handling manually verified. `go test -race` clean. Fleet keys workers by task; needs-you sorted to top.
