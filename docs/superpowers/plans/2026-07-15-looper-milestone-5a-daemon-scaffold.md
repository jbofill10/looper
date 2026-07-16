# looper Milestone 5a — Daemon Scaffold + gRPC Transport + Auto-Spawn

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Strict TDD. Checkbox steps.

**Goal:** A `looperd` daemon that serves the gRPC `Looper` service (Ping, Shutdown) over a Unix socket, a client dial helper, tmux-style auto-spawn (client starts the daemon if not running), and `looper daemon` / `looper ping` / `looper shutdown` CLI commands.

**Architecture:** The generated gRPC stubs already exist in package `rpc` (committed). New `daemon` package implements the service + serving lifecycle. New `client` package dials the socket and ensures the daemon is running (auto-spawn). CLI gains three commands. No loop logic yet — that is M5b. This proves transport + lifecycle end to end.

**Tech Stack:** Go 1.26, `google.golang.org/grpc`, generated `rpc` package. No new deps.

## Global Constraints

- Module `github.com/jbofill10/looper`; Go 1.26. PR into `main`.
- Socket path resolution (shared helper, put in `client` or a small `paths` file): `$XDG_RUNTIME_DIR/looper.sock` if `XDG_RUNTIME_DIR` set, else `filepath.Join(os.TempDir(), fmt.Sprintf("looper-%d.sock", os.Getuid()))`.
- Daemon version string: reuse a package-level `const Version = "0.1.0-dev"` (define in `daemon`).
- Generated code in `rpc/` is committed; regenerate via `scripts/gen-proto.sh` only when the proto changes.

---

### Task 1: `daemon` — service implementation + serving

**Files:** Create `daemon/daemon.go`; Test `daemon/daemon_test.go`.

**Interfaces produced:**
- `const daemon.Version = "0.1.0-dev"`.
- `type Server struct{ ... }` embedding `rpc.UnimplementedLooperServer`; implements:
  - `Ping(ctx, *rpc.PingRequest) (*rpc.PingResponse, error)` → `{Version: Version}`.
  - `Shutdown(ctx, *rpc.ShutdownRequest) (*rpc.ShutdownResponse, error)` → triggers graceful stop (signal an internal channel) and returns empty.
- `func daemon.New() *Server`.
- `func (s *Server) Serve(socketPath string) error` — remove any stale socket file, `net.Listen("unix", socketPath)`, create a `grpc.Server`, register, and serve. Return when the server stops (via `Shutdown` RPC or `Stop()`). On stop, remove the socket file.
- `func (s *Server) Stop()` — `grpc.Server.GracefulStop()` (safe if not serving).

**Test cases (in-process, real unix socket):**
- Start `Serve` on a `t.TempDir()` socket in a goroutine; dial with `grpc.NewClient`/`grpc.Dial` over unix; `Ping` returns `Version`; then `Shutdown` returns and `Serve` unblocks; socket file removed afterward.
- `Serve` on a path where a stale socket file exists succeeds (it removes the stale file first).

Use a bounded retry to wait until the socket is dialable before the first RPC (poll `net.Dial`), no fixed sleeps.

- [ ] Step 1: test. Step 2: FAIL. Step 3: implement. Step 4: `go test -race ./daemon/...` PASS. Step 5: commit `feat(daemon): grpc server with ping and shutdown`.

---

### Task 2: `client` — dial + auto-spawn

**Files:** Create `client/client.go`; Test `client/client_test.go`.

**Interfaces produced:**
- `func client.SocketPath() string` — the resolution above.
- `func client.Dial(socketPath string) (rpc.LooperClient, *grpc.ClientConn, error)` — dial unix (`grpc.NewClient("unix://"+abspath, insecure creds)`); caller closes the conn.
- `func client.EnsureDaemon(socketPath, looperBin string) error`:
  1. Try `Dial` + `Ping` (short timeout). If it succeeds, return nil (already running).
  2. Else spawn `looperBin daemon --socket <socketPath>` detached: `exec.Command`, `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid:true}`, stdout/stderr to a log file under the socket's dir (`<socket>.log`), `cmd.Start()` (do not Wait).
  3. Poll `Dial`+`Ping` with a bounded retry (e.g. up to ~3s) until it responds; return an error if it never comes up.

**Test cases:**
- `SocketPath` respects `XDG_RUNTIME_DIR` (set via `t.Setenv`) and falls back when unset.
- `EnsureDaemon` "already running" path: start an in-process `daemon.Serve` on a temp socket, call `EnsureDaemon` with a bogus `looperBin` — it must return nil WITHOUT spawning (proves it detected the running daemon; a bogus bin would fail if it tried to spawn).
- Auto-spawn path is covered by the CLI integration smoke in Task 3 (spawning a real process is not a good unit test); note this.

- [ ] Step 1: test. Step 2: FAIL. Step 3: implement. Step 4: `go test -race ./client/...` PASS. Step 5: commit `feat(client): dial helper and daemon auto-spawn`.

---

### Task 3: CLI commands + integration smoke

**Files:** Create `cli/daemon.go`; modify `cli/root.go`; Test `cli/daemon_test.go`.

**Commands:**
- `looper daemon [--socket <path>]` — runs `daemon.New().Serve(socket)` in the foreground (default socket = `client.SocketPath()`). This is what auto-spawn invokes and what you run to debug.
- `looper ping [--socket <path>]` — `client.EnsureDaemon(socket, self)` then `Dial`+`Ping`; print the version. `self` from `os.Executable()`.
- `looper shutdown [--socket <path>]` — `Dial` (do NOT auto-spawn); call `Shutdown`; print confirmation. If the daemon isn't running, print a friendly "not running" and exit 0.
- Register all three in `newRootCmd`.

**Integration smoke test (real built binary):**
- In `cli/daemon_test.go`, build the looper binary to a temp path with `go build` (via `exec.Command("go","build",...)`), then run `<bin> daemon --socket <tmp>` as a detached process, poll until pingable using `client.Dial`+`Ping`, assert version, then run `<bin> shutdown --socket <tmp>` and assert the process exits and the socket is removed. Skip with `t.Skip` if `go build` is unavailable. This exercises the real auto-spawn-adjacent path end to end.

- [ ] Step 1: test. Step 2: FAIL. Step 3: implement. Step 4: `go build ./... && go test -race ./cli/...` PASS. Step 5: commit `feat(cli): daemon, ping, and shutdown commands`.

---

## Self-Review

- gRPC service (Ping/Shutdown) + serving lifecycle → Task 1.
- Dial + auto-spawn → Task 2.
- CLI + real end-to-end smoke → Task 3.
- Transport + lifecycle proven; no loop logic yet (M5b). Generated rpc code already committed.
