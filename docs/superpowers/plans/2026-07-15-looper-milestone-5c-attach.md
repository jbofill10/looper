# looper Milestone 5c — PTY Attach Across the Socket + Daemon Interactive Steps

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Strict TDD. Checkbox steps.

**Goal:** Interactive steps run inside the daemon (PTY owned by the daemon, not auto-attached), and a client can attach to a run's live session over the gRPC `Attach` bidi stream — sending stdin + resize, receiving terminal output — with tmux-style detach. `looper attach <run-id>`.

**Architecture:** `pty.Session` gains an output tap (`PipeTo`). `runner.Worker`/`InteractiveExecutor` gain an injectable interactive-run func so the daemon can run the session in daemon mode (register for attach) instead of the local auto-attach. `daemon.Manager` stops rejecting interactive and registers each live session on its run. The gRPC `Attach` handler bridges a client stream to the session. The `looper attach` client reuses the M4 `detachScanner` for the detach escape. Generated `rpc` Attach stubs are already committed.

**Testability:** The `PipeTo` tap and the daemon Attach bridge are integration-tested with a real PTY running `cat` (echoes stdin→stdout) over a real gRPC socket — no `claude` needed. The client's raw-mode terminal handling is manually verified (as in M4).

**Tech Stack:** Go 1.26, `google.golang.org/grpc`, `golang.org/x/term`. No new deps.

## Global Constraints

- Module `github.com/jbofill10/looper`; Go 1.26. PR into `main`. `go test -race ./...` clean.
- Single attacher per session is sufficient (the live-writer slot is single). A second concurrent Attach may replace or be rejected — reject with a clear error is simplest.
- Detach escape `Ctrl-b d` is handled CLIENT-side (reuse `pty` detach scanner); on detach the client closes its send direction and restores the terminal; the daemon keeps the session running.

---

### Task 1: `pty.Session.PipeTo` output tap

**Files:** Modify `pty/session.go`; add to `pty/session_test.go`.

**Interface produced:**
- `func (s *Session) PipeTo(w io.Writer) (stop func())` — write the current `Scrollback()` to `w`, then set the live writer to `w` (mutex-guarded, reusing the existing swappable live-writer mechanism); `stop()` clears the live writer (idempotent). If a live writer is already set, `PipeTo` replaces it (document this).

**Test cases:** run `["cat"]`; `PipeTo(&buf)`; `Write([]byte("ping\n"))`; spin-wait until `buf` contains `ping`; call `stop()`; further session output no longer reaches `buf`. Scrollback replay: write before PipeTo, assert the pre-existing output appears in `buf` after PipeTo.

- [ ] TDD → `go test -race ./pty/...` → commit `feat(pty): PipeTo output tap for remote attach`.

---

### Task 2: injectable interactive run in the worker/executor

**Files:** Modify `runner/interactive.go`, `runner/worker.go`, tests.

**Changes:**
- `Worker` gains `InteractiveRun func(argv, env []string, socketPath string) error`.
- In `executorFor(StepInteractive)`, set the constructed `InteractiveExecutor.run = w.InteractiveRun` (when non-nil); nil ⇒ default local `runPTY` (unchanged).
- No other behavior change; local `looper run` interactive path is identical.

**Test cases:** `executorFor` returns an `*InteractiveExecutor` whose `run` is the injected func when `Worker.InteractiveRun` is set (assert it runs by having the injected func set a flag / send on a channel through a full executor `Run` with a fake harness + FakePrompter).

- [ ] TDD → commit `feat(runner): injectable interactive run for daemon mode`.

---

### Task 3: daemon runs interactive sessions + registers them for attach

**Files:** Modify `daemon/manager.go`; Test additions in `daemon/manager_test.go`.

**Changes:**
- Remove the interactive-rejection in `StartLoop`.
- The per-run struct gains a mutex-guarded `session *pty.Session` with `setSession`/`clearSession`/`currentSession` accessors.
- When building the worker for a run, set `Worker.InteractiveRun` to a daemon impl:
  ```
  func(argv, env, socketPath) error {
      dir := workdirFromEnv(env)           // read WORKDIR= entry
      sess, err := pty.Start(pty.Config{Argv: argv, Env: env, Dir: dir})
      if err != nil { return err }
      run.setSession(sess); defer run.clearSession()
      publish an Update{Kind:"state", State:"session_live"} for visibility
      err = sess.Wait(); sess.Close(); return err
  }
  ```
- Add `func (m *Manager) Session(runID string) (*pty.Session, bool)` for the Attach handler.

**Test cases:** StartLoop a loop whose single interactive step uses harness interactive `["sh","-c","cat"]` (echoes; BuildInteractive's appended `--settings <file> <prompt>` args are ignored by `cat`); poll `Manager.Session(runID)` until non-nil; write to the session and read via a `PipeTo` buffer to confirm it's the live session; `StopLoop` → session killed, run ends. (This validates daemon interactive execution + session registration without gRPC.)

- [ ] TDD → `go test -race ./daemon/...` → commit `feat(daemon): run interactive sessions and register them for attach`.

---

### Task 4: `Attach` gRPC bridge

**Files:** Modify `daemon/service.go`; Test `daemon/attach_test.go`.

**Handler `Attach(stream)`:**
1. `Recv()` the first message; require `AttachStart` with a run id; look up the session via `Manager.Session`; if absent, return a `codes.NotFound`/`FailedPrecondition` error.
2. `stop := sess.PipeTo(streamWriter{stream})`; `defer stop()`, where `streamWriter.Write(p)` does `stream.Send(&rpc.AttachOutput{Data: p})`.
3. Loop `Recv()`: `data` → `sess.Write(data)`; `resize` → `sess.Resize(rows, cols)`; on `io.EOF` (client closed send) or `stream.Context().Done()` → return nil.
4. Concurrency: the output tap writes from the session's reader goroutine while the Recv loop runs; ensure `streamWriter.Send` and the Recv loop don't violate gRPC's "one sender / one receiver" rule — only the tap sends, only the handler receives, so that's fine. Guard nothing else.

**Test cases (real gRPC over unix socket):** StartLoop an interactive `cat` loop; wait for the session; open `Attach`; send `AttachStart{run_id}` then `data:"hello\n"`; receive `AttachOutput` and assert bytes contain `hello`; send a `resize`; close send; assert the handler returns. Attach to a nonexistent run → error.

- [ ] TDD → `go test -race ./daemon/...` → commit `feat(daemon): attach bidi bridge to live sessions`.

---

### Task 5: `looper attach` client

**Files:** Create `cli/attach.go`; modify `cli/root.go`; Test `cli/attach_test.go` (limited).

**Command `looper attach <run-id> [--socket]`:**
1. `Dial`; open `Attach` stream; send `AttachStart{run_id}`.
2. If stdin is a terminal, `term.MakeRaw`; `defer Restore`. Send initial `Resize` from `term.GetSize`; on `SIGWINCH` send updated size.
3. Goroutine: read `os.Stdin` → `detachScanner` → send `data`; on detach → `stream.CloseSend()` and signal done.
4. Main: `Recv()` loop → write `AttachOutput.Data` to `os.Stdout`; on stream end or detach → restore and return.
5. Print a hint line on attach (`-- attached; Ctrl-b d to detach --`) to stderr before raw mode.
- Register in `newRootCmd`.

**Test note:** raw-mode is manual-only (as M4). Add a non-TTY smoke: with stdin a pipe (not a terminal), `attach` to a live `cat` run via the built binary, pipe `hi\n` then close → the process should stream output and exit without error. `t.Skip` if `go build` unavailable. Keep this light; the daemon-side bridge is already covered in Task 4.

- [ ] TDD → `go build ./... && go test -race ./cli/...` → commit `feat(cli): attach command for live sessions`.

---

## Self-Review

- Output tap → Task 1. Injectable interactive run → Task 2. Daemon interactive + session registry → Task 3. Attach bridge → Task 4. Client → Task 5.
- Interactive now works both locally (`looper run`, auto-attach) and daemon-managed (`looper start` + `looper attach`). `go test -race` clean; attach bridge tested with `cat` over real gRPC; client raw-mode manual.
