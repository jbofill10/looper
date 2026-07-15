# looper Milestone 4 — PTY Ownership + Attach/Detach

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Strict TDD. Checkbox steps.

**Goal:** A `pty` package that runs a command in a looper-owned pseudoterminal, captures scrollback, supports resize and input, and provides a tmux-style raw-mode attach/detach bridge. Wire it into `InteractiveExecutor` so interactive steps run in a looper-owned PTY with the human auto-attached.

**Architecture:** New `pty` package: `Session` (runs argv in a PTY via `creack/pty`, background reader tees output to a ring-buffer scrollback + an optional live writer), a pure `detachScanner` state machine for the `Ctrl-b d` escape, and `Attach` (raw-mode bridge between the real terminal and the session). `InteractiveExecutor` gains a PTY-backed `run` implementation used by default; the M3 hook-driven state detection is unchanged (hooks still fire; looper still owns the socket). This PTY primitive is transport-agnostic and is exactly what the daemon (M5) will own and proxy over gRPC.

**What is and isn't auto-tested:** `Session` mechanics (start/capture/write/wait/resize) and the `detachScanner` are unit-tested with the PTY itself as the tty — no real TTY on stdin needed. The raw-mode `Attach` glue (which calls `term.MakeRaw` on a real terminal) cannot be unit-tested in CI without a controlling terminal; it is structured thinly around the tested scanner and verified manually. This is called out honestly; do not fake a TTY to claim coverage.

**Tech Stack:** Go 1.26; new deps `github.com/creack/pty`, `golang.org/x/term`.

## Global Constraints

- Module `github.com/jbofill10/looper`; Go 1.26. PR into `main`.
- Detach sequence: `Ctrl-b` (0x02) then `d` (0x64). A lone `Ctrl-b` not followed by `d` is passed through (both bytes) to the session.
- Scrollback ring buffer default 64 KiB.
- Concurrency: the background reader goroutine is the only writer to the ring buffer and the live writer; guard the swappable live writer with a mutex. Must pass `go test -race`.

---

### Task 1: dependencies + `detachScanner` (pure)

**Files:** Create `pty/detach.go`; Test `pty/detach_test.go`.

- [ ] Step 1: `go get github.com/creack/pty@latest golang.org/x/term@latest`.
- [ ] Step 2: Write `pty/detach_test.go`.

**Interface produced:**
- `type detachScanner struct{ armed bool }`
- `func (d *detachScanner) scan(in []byte) (passthrough []byte, detached bool)` — walks `in` byte by byte:
  - if `armed` and byte==`d` → set `detached=true`, stop consuming (drop the `d`), return passthrough so far.
  - if `armed` and byte!=`d` → emit a literal `Ctrl-b` then this byte to passthrough, disarm.
  - if not armed and byte==`Ctrl-b` (0x02) → arm, emit nothing yet.
  - else → emit the byte.
  - End of input while armed → stay armed (state persists across calls); emit nothing for the dangling Ctrl-b until next call resolves it.

**Test cases:** plain bytes pass through; `Ctrl-b d` → detached, no passthrough for the escape; `Ctrl-b x` → passthrough `[0x02,'x']`, not detached; split across calls (`Ctrl-b` in call 1, `d` in call 2) → detached on call 2; `Ctrl-b` then non-d in next call → passthrough both.

- [ ] Step 3: FAIL. Step 4: implement `pty/detach.go`. Step 5: PASS. Step 6: commit `feat(pty): detach escape scanner`.

---

### Task 2: `pty.Session`

**Files:** Create `pty/session.go`; Test `pty/session_test.go`.

**Interfaces produced:**
- `type Config struct { Argv []string; Env []string; Dir string; ScrollbackBytes int }`
- `func Start(cfg Config) (*Session, error)` — `exec.Command(argv[0], argv[1:]...)`, set Env/Dir, `pty.Start(cmd)` to get the ptmx `*os.File`; start a reader goroutine copying ptmx→(ring buffer + live writer). Default ScrollbackBytes=64Ki when 0.
- `func (s *Session) Write(p []byte) (int, error)` — write input to the ptmx.
- `func (s *Session) Scrollback() []byte` — snapshot of the ring buffer (mutex-guarded copy).
- `func (s *Session) setLive(w io.Writer)` / internal — swap the live writer (nil to disable); mutex-guarded. (Exported as needed by Attach in same package.)
- `func (s *Session) Resize(rows, cols uint16) error` — `pty.Setsize`.
- `func (s *Session) Wait() error` — wait for process exit; ensures reader goroutine drains and exits.
- `func (s *Session) Close() error` — close ptmx, kill process if still running, idempotent.

**Test cases (PTY is the tty — no real terminal needed):**
- Start `["sh","-c","printf hello; read line; printf \"got:%s\" \"$line\""]`; after a brief spin-wait until scrollback contains `hello`, `Write([]byte("world\n"))`, `Wait()`, assert scrollback contains `got:world`. (Spin-wait by polling `Scrollback()` with a bounded loop + tiny sleeps; acceptable in a test.)
- Scrollback cap: run a command emitting > ScrollbackBytes; assert `len(Scrollback()) <= ScrollbackBytes`.
- `Resize(40,120)` returns nil.
- `Close()` twice does not panic; `Wait()` after normal exit returns nil.

- [ ] Step 1: test. Step 2: FAIL. Step 3: implement. Step 4: `go test -race ./pty/...` PASS. Step 5: commit `feat(pty): looper-owned session with scrollback capture`.

---

### Task 3: `Attach` (raw-mode bridge)

**Files:** Create `pty/attach.go`; Test `pty/attach_test.go` (limited — see note).

**Interface produced:**
- `func (s *Session) Attach(in, out *os.File) error`:
  1. If `term.IsTerminal(int(in.Fd()))`, `oldState, _ := term.MakeRaw(int(in.Fd()))`; `defer term.Restore(...)`.
  2. Write current `Scrollback()` to `out`, then `setLive(out)`; `defer setLive(nil)`.
  3. Best-effort initial `Resize` from `term.GetSize(out.Fd())`.
  4. Loop in a goroutine: read from `in` into a buf; run bytes through a `detachScanner`; write passthrough to `s.Write`; if `detached`, stop and return.
  5. Also stop when the session exits (select on a session-done signal). Return nil on detach or session exit.

**Test note:** `Attach` requires a real terminal for the raw-mode path; it is NOT unit-tested end-to-end. The unit-testable behavior (byte routing + detach) already lives in `detachScanner` (Task 1). Add only a compile-level/no-TTY guard test: calling `Attach` with a non-terminal `in` (e.g. an `os.Pipe` read end) must NOT error on the MakeRaw branch (it should skip raw mode) — feed a few bytes then the detach sequence via the pipe and assert `Attach` returns nil. This exercises the routing loop without a TTY.

- [ ] Step 1: test (non-TTY routing). Step 2: FAIL. Step 3: implement. Step 4: `go test -race ./pty/...` PASS. Step 5: commit `feat(pty): raw-mode attach/detach bridge`.

---

### Task 4: PTY-backed interactive run

**Files:** Modify `runner/interactive.go`, `runner/interactive_test.go`.

**Changes:**
- Add a default PTY run implementation `runPTY(argv, env []string, socketPath string) error` used when `InteractiveExecutor.run == nil` (replacing `execInteractive` as the default; keep `execInteractive` removed or unused — do not leave dead code):
  1. Derive `Dir` from the `WORKDIR` env entry.
  2. `sess, err := pty.Start(pty.Config{Argv:argv, Env:env, Dir:dir})`.
  3. `go func(){ sess.Attach(os.Stdin, os.Stdout) }()` — auto-attach the human.
  4. `err = sess.Wait()`; `sess.Close()`; return err.
- The injectable `run` path (fake) is unchanged, so all M3 executor tests still pass without a PTY.
- No behavior change to state/outcome resolution.

**Test cases:** existing M3 interactive tests (fake `run`) still pass. Add a test that when `run==nil` the executor selects the PTY path — assert indirectly by running a trivial non-interactive argv through the real default path in a test ONLY if a PTY can be allocated in the environment; otherwise keep the default-path coverage to a construction check. Prefer: a test using the real default `run` with argv `["sh","-c","true"]` and a harness with no hooks — it should start a pty, the session exits immediately, `Wait` returns, outcome falls to the Prompter (FakePrompter advance). Guard with a skip if `pty.Start` errors (headless CI without pty support): `t.Skip`.

- [ ] Step 1: test. Step 2: FAIL. Step 3: implement. Step 4: `go test -race ./...` PASS. Step 5: commit `feat(runner): run interactive sessions in a looper-owned PTY`.

---

## Self-Review

- detach scanner (pure, tested) → Task 1. Session mechanics (tested via pty) → Task 2. Attach bridge (thin, non-TTY routing tested; raw-mode manual) → Task 3. Executor integration (default PTY path, M3 tests intact) → Task 4.
- `go test -race ./...` clean. Honest about the raw-mode Attach path being manually verified.
- Daemon/gRPC (M5) will own the Session and replace the local auto-attach with a socket-proxied attach; the `Session` + `detachScanner` + `Attach` primitive is reused there.
