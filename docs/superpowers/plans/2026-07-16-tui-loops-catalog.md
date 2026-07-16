# TUI Loops Catalog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Loops section to the Fleet TUI: browse every configured loop, toggle it enabled (persisted, auto-resumes on daemon restart), run it once, gracefully or hard-stop it, rename/delete it, and add/edit/delete/reorder its steps inline — reusing the existing step-authoring builder engine.

**Architecture:** A daemon-wide JSON registry (next to the socket file) tracks which `(base_dir, loop_name)` pairs are enabled; `Manager` gains `ListLoops`/`SetLoopEnabled`/`RunLoopOnce`/`StopLoopGraceful`/`RenameLoop`/`DeleteLoop`/`AutoResume` methods and new proto RPCs expose them. `runner.Worker` gains a `GracefulStop` channel checked only at iteration boundaries. The TUI's `Model` gains a Loops tree above the existing worker table; each loop's step list is an embedded `builder.Model`, rendered inline instead of full-screen, so add/edit/delete/reorder logic is not duplicated.

**Tech Stack:** Go 1.26, Bubble Tea/lipgloss (TUI), gRPC + protobuf (`proto/looper.proto`, regenerated via `scripts/gen-proto.sh`), `gopkg.in/yaml.v3` (loop files), stdlib `encoding/json` (registry).

## Global Constraints

- Every change goes through a branch + PR into `main` (see repo `CLAUDE.md`) — this plan assumes it executes on its own branch, e.g. `feat/tui-loops-catalog`.
- `go build ./...` and `go test ./...` must pass after every task.
- Design spec: `docs/superpowers/specs/2026-07-16-tui-loops-catalog-design.md` — every requirement there must map to a task below.
- No placeholders: every step's code is complete and compiles as shown.

---

### Task 1: Runner graceful-stop flag

**Files:**
- Modify: `runner/worker.go`
- Test: `runner/worker_test.go` (existing file — add a test function; if it doesn't exist, create it)

**Interfaces:**
- Produces: `Worker.GracefulStop <-chan struct{}` field. When non-nil and closed, `Worker.run()` returns `nil` after the in-flight iteration completes, before starting the next one — it does not touch `Worker.Ctx` and does not interrupt an in-flight step.

- [ ] **Step 1: Check for an existing worker test file and its patterns**

Run: `ls runner/*_test.go`

Read whichever file covers `Worker.Run`/`Worker.run` (likely `runner/worker_test.go`) to match its existing helper style (temp dirs, minimal `config.Loop`/`config.Step` literals) before adding the new test.

- [ ] **Step 2: Write the failing test**

Append to `runner/worker_test.go`:

```go
func TestWorker_GracefulStopEndsAfterCurrentIteration(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:  "l",
		Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	graceful := make(chan struct{})
	var iterations []int

	w := &Worker{
		Loop:         loop,
		BaseDir:      filepath.Join(dir, ".looper"),
		Workdir:      dir,
		GracefulStop: graceful,
		OnReport: func(r Report) {
			if r.Kind == ReportIteration {
				iterations = append(iterations, r.Iteration)
				if r.Iteration == 1 {
					close(graceful)
				}
			}
		},
	}

	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(iterations) != 1 || iterations[0] != 1 {
		t.Errorf("iterations reported = %v, want [1] (graceful stop must not prevent iteration 1 from running, but must prevent iteration 2)", iterations)
	}
}
```

Ensure the test file imports `"path/filepath"` and `"testing"` and `"github.com/jbofill10/looper/config"` (add any missing from the existing import block).

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./runner/... -run TestWorker_GracefulStopEndsAfterCurrentIteration -v`
Expected: FAIL — build error, `Worker` has no field `GracefulStop` (or the test never stops because the loop runs unbounded iterations until timeout — either way, a clear failure, not a pass).

- [ ] **Step 4: Add the field and the check**

In `runner/worker.go`, add the field to the `Worker` struct (near `Ctx`):

```go
	// GracefulStop, if set, is checked once per iteration boundary (before
	// starting the next iteration). Once closed, Run returns nil after the
	// in-flight iteration completes — unlike Ctx, it never cancels a
	// context or interrupts a step already in progress.
	GracefulStop <-chan struct{}
```

Add a helper method near `ctxErr`:

```go
// gracefulStopped reports whether w.GracefulStop is set and closed.
func (w *Worker) gracefulStopped() bool {
	if w.GracefulStop == nil {
		return false
	}
	select {
	case <-w.GracefulStop:
		return true
	default:
		return false
	}
}
```

In `run()`, add the check right after the existing `ctxErr` check:

```go
	for iter := 1; w.Loop.MaxIterations == 0 || iter <= w.Loop.MaxIterations; iter++ {
		if err := w.ctxErr(); err != nil {
			return err
		}
		if w.gracefulStopped() {
			return nil
		}
		id := gen()
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./runner/... -run TestWorker_GracefulStopEndsAfterCurrentIteration -v`
Expected: PASS

- [ ] **Step 6: Run the full runner package test suite**

Run: `go test ./runner/...`
Expected: PASS (no regressions)

- [ ] **Step 7: Commit**

```bash
git add runner/worker.go runner/worker_test.go
git commit -m "feat(runner): add Worker.GracefulStop, checked at iteration boundaries"
```

---

### Task 2: Shared lenient loop loader in `config`

**Why first:** both the builder (existing) and the new daemon `ListLoops` need to load a loop file without `config.LoadLoop`'s strict whole-file `Validate` (which rejects a file with zero steps or one invalid step) — a mid-edit loop must still show up in the catalog. Deduplicating this now avoids two copies of the same parsing logic.

**Files:**
- Modify: `config/loop.go`
- Modify: `builder/builder.go` (replace its private `loadLoopLenient` with the new shared one)
- Test: `config/loop_test.go` (existing file — add to it)

**Interfaces:**
- Produces: `config.LoadLoopLenient(path string) (*Loop, error)` — reads and YAML-unmarshals the file, returning `*Loop` and an `os.ErrNotExist`-wrapped error if missing, WITHOUT calling `Validate`.
- Consumes (by later tasks): `daemon.Manager.ListLoops` (Task 5) calls `config.LoadLoopLenient`.

- [ ] **Step 1: Write the failing test**

Add to `config/loop_test.go` (create it if it doesn't exist, matching the package's existing test conventions — check with `ls config/*_test.go` first):

```go
func TestLoadLoopLenient_AllowsZeroStepsAndMissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "l.yaml")
	if err := os.WriteFile(path, []byte("name: l\nsteps: []\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	loop, err := LoadLoopLenient(path)
	if err != nil {
		t.Fatalf("LoadLoopLenient: %v", err)
	}
	if loop.Name != "l" || len(loop.Steps) != 0 {
		t.Errorf("loop = %+v, want Name=l Steps=[]", loop)
	}
}

func TestLoadLoopLenient_MissingFileReturnsNotExist(t *testing.T) {
	_, err := LoadLoopLenient(filepath.Join(t.TempDir(), "missing.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want wrapping os.ErrNotExist", err)
	}
}
```

Add `"errors"`, `"os"`, `"path/filepath"`, `"testing"` to the test file's imports as needed.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./config/... -run TestLoadLoopLenient -v`
Expected: FAIL — `LoadLoopLenient` undefined.

- [ ] **Step 3: Implement it in `config/loop.go`**

Add below `LoadLoop`:

```go
// LoadLoopLenient reads and YAML-parses the loop file at path without
// requiring it to pass Validate (which rejects a whole file over a single
// invalid step, or zero steps). Per-step/whole-loop validity is instead a
// caller concern (e.g. the builder's per-step error surfacing, or the
// Loops catalog showing a mid-edit loop rather than hiding it). Returns an
// error wrapping os.ErrNotExist if the file doesn't exist.
func LoadLoopLenient(path string) (*Loop, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read loop file %q: %w", path, err)
	}
	var l Loop
	if err := yaml.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parse loop file %q: %w", path, err)
	}
	return &l, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./config/... -run TestLoadLoopLenient -v`
Expected: PASS

- [ ] **Step 5: Point `builder.New` and its reload path at the shared helper**

In `builder/builder.go`, delete the private `loadLoopLenient` function entirely, and replace its two call sites (`New`, and the `SessionDoneMsg` handler in `Update`) with `config.LoadLoopLenient`. The only behavior difference: the error text changes from `"parse loop file %q: %w"` (unwrapped read errors previously had no context) to the new helper's wording — this is cosmetic only, no test in `builder` asserts on that literal string (verify with `grep -rn "parse loop file\|read loop file" builder/*_test.go`; if a test does assert on it, update that assertion to match).

- [ ] **Step 6: Run the full builder + config test suites**

Run: `go test ./builder/... ./config/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add config/loop.go config/loop_test.go builder/builder.go
git commit -m "refactor(config): share LoadLoopLenient between builder and daemon"
```

---

### Task 3: Daemon-wide enabled-loops registry

**Files:**
- Create: `daemon/registry.go`
- Test: `daemon/registry_test.go`

**Interfaces:**
- Produces:
  - `type registryEntry struct { BaseDir, Workdir, LoopName string; Enabled bool }`
  - `registryKey(baseDir, loopName string) string`
  - `loadRegistry(path string) (map[string]registryEntry, error)` — empty map, nil error if the file doesn't exist.
  - `saveRegistry(path string, entries map[string]registryEntry) error`
  - `defaultRegistryPath() string`
- Consumes (by Task 5): `Manager` will hold a `registryPath string` and call these.

- [ ] **Step 1: Write the failing test**

Create `daemon/registry_test.go`:

```go
package daemon

import (
	"path/filepath"
	"testing"
)

func TestRegistry_LoadMissingFileReturnsEmptyMap(t *testing.T) {
	entries, err := loadRegistry(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %v, want empty", entries)
	}
}

func TestRegistry_SaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := map[string]registryEntry{
		registryKey("/proj/.looper", "jira-tracker"): {
			BaseDir: "/proj/.looper", Workdir: "/proj", LoopName: "jira-tracker", Enabled: true,
		},
	}
	if err := saveRegistry(path, want); err != nil {
		t.Fatalf("saveRegistry: %v", err)
	}
	got, err := loadRegistry(path)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	key := registryKey("/proj/.looper", "jira-tracker")
	if got[key] != want[key] {
		t.Errorf("loaded entry = %+v, want %+v", got[key], want[key])
	}
}

func TestRegistryKey_DistinguishesBaseDir(t *testing.T) {
	a := registryKey("/proj1/.looper", "jira-tracker")
	b := registryKey("/proj2/.looper", "jira-tracker")
	if a == b {
		t.Errorf("keys collided for different base dirs: %q", a)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./daemon/... -run TestRegistry -v`
Expected: FAIL — `registryEntry`/`loadRegistry`/etc. undefined.

- [ ] **Step 3: Implement `daemon/registry.go`**

```go
// Package daemon: registry.go implements the daemon-wide enabled-loops
// registry. looperd is a single per-user process shared across however
// many project directories invoke it (see client.SocketPath's per-uid
// path), so a per-project state file would be invisible to the daemon on
// its own restart. Instead, one registry file — resolved the same way as
// the socket path — tracks every (base_dir, loop_name) pair's enabled
// flag, so AutoResume can restart them without discovering project
// directories.
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// registryEntry is one loop's persisted enablement record.
type registryEntry struct {
	BaseDir  string `json:"baseDir"`
	Workdir  string `json:"workdir"`
	LoopName string `json:"loopName"`
	Enabled  bool   `json:"enabled"`
}

// registryFile is registry.json's on-disk shape.
type registryFile struct {
	Loops map[string]registryEntry `json:"loops"`
}

// registryKey identifies a loop within the registry: a loop name is only
// unique within its own project, so the key includes base_dir.
func registryKey(baseDir, loopName string) string {
	return baseDir + "|" + loopName
}

// defaultRegistryPath returns the daemon-wide registry file's path,
// resolved the same way client.SocketPath resolves the socket path.
// Duplicated rather than imported from package client to avoid daemon
// depending on the CLI-facing client package for one path computation
// (the same tradeoff tui/program.go's globalConfigPath already makes).
func defaultRegistryPath() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "looper-state.json")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("looper-state-%d.json", os.Getuid()))
}

// loadRegistry reads and parses the registry file at path, returning an
// empty map (not an error) if the file doesn't exist yet.
func loadRegistry(path string) (map[string]registryEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]registryEntry{}, nil
		}
		return nil, fmt.Errorf("read registry %q: %w", path, err)
	}
	var rf registryFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parse registry %q: %w", path, err)
	}
	if rf.Loops == nil {
		rf.Loops = map[string]registryEntry{}
	}
	return rf.Loops, nil
}

// saveRegistry writes entries to path as JSON, creating any missing parent
// directories.
func saveRegistry(path string, entries map[string]registryEntry) error {
	data, err := json.MarshalIndent(registryFile{Loops: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create registry directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write registry %q: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./daemon/... -run TestRegistry -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/registry.go daemon/registry_test.go
git commit -m "feat(daemon): add daemon-wide enabled-loops registry"
```

---

### Task 4: Manager tracks base_dir/workdir per run + StopLoopGraceful

**Files:**
- Modify: `daemon/manager.go`
- Modify: `daemon/manager_test.go` (add `SetRegistryPath` to the shared `newTestManager` helper so every existing test stays hermetic — see Step 1)

**Interfaces:**
- Produces:
  - `runEntry.baseDir`, `runEntry.workdir string` fields, set by `StartLoop`.
  - `runEntry.graceful chan struct{}` + `runEntry.gracefulOnce sync.Once`, wired into each `runner.Worker.GracefulStop`.
  - `func (m *Manager) StopLoopGraceful(runID string) error`
  - `func (m *Manager) SetRegistryPath(path string)` — overrides the default (`defaultRegistryPath()`), used by tests.
- Consumes: Task 3's `registryEntry`/`loadRegistry`/`saveRegistry`/`defaultRegistryPath` (not yet called here — that's Task 5 — but `SetRegistryPath` is added now since it touches the same `Manager` struct literal).

- [ ] **Step 1: Update the shared test helper first (no behavior change yet)**

In `daemon/manager_test.go`, change `newTestManager`:

```go
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(nil, "looper")
	m.SetRegistryPath(filepath.Join(t.TempDir(), "state.json"))
	return m
}
```

(This references `SetRegistryPath`, which doesn't exist yet — that's expected; Step 3 adds it. This ordering keeps the diff in one place instead of touching the test file twice.)

- [ ] **Step 2: Run the package tests to confirm the expected failure**

Run: `go test ./daemon/... -run TestManager -v`
Expected: FAIL — build error, `Manager` has no method `SetRegistryPath`.

- [ ] **Step 3: Add the registry path field + setter, and per-run baseDir/workdir/graceful state**

In `daemon/manager.go`, add to the `runEntry` struct (near `cancel`):

```go
	baseDir string // the .looper dir this run was started from
	workdir string // execution dir this run was started from

	graceful     chan struct{}
	gracefulOnce sync.Once
```

Add to the `Manager` struct (near `looperBin`):

```go
	registryPath string
```

In `NewManager`, initialize it:

```go
	return &Manager{
		global:       global,
		looperBin:    looperBin,
		newID:        newCounter("run"),
		newReqID:     newCounter("req"),
		runs:         map[string]*runEntry{},
		subs:         map[int]*subscriber{},
		registryPath: defaultRegistryPath(),
	}
```

Add the setter below `NewManager`:

```go
// SetRegistryPath overrides the daemon-wide enabled-loops registry path
// (defaultRegistryPath() otherwise). Tests use this to avoid touching the
// real per-user registry file.
func (m *Manager) SetRegistryPath(path string) {
	m.registryPath = path
}
```

In `StartLoop`, populate the new `runEntry` fields and construct the graceful channel (the `re := &runEntry{...}` literal):

```go
	re := &runEntry{
		info:             RunInfo{RunID: runID, LoopName: loop.Name, Status: "running"},
		cancel:           cancel,
		fanIn:            make(chan Update, 256),
		pending:          map[string]*pendingRequest{},
		workers:          map[string]*workerState{},
		workersRemaining: n,
		baseDir:          baseDir,
		workdir:          workdir,
		graceful:         make(chan struct{}),
	}
```

Wire it into each `runner.Worker` constructed in `StartLoop`'s loop:

```go
		w := &runner.Worker{
			Loop:        loop,
			BaseDir:     baseDir,
			Workdir:     workdir,
			Prompter:    prompter,
			Global:      m.global,
			LooperBin:   m.looperBin,
			Ctx:         ctx,
			ID:          workerID,
			TaskVar:     loop.TaskVar,
			AcquireLock: acquireLock,
			GracefulStop: re.graceful,
			InteractiveRun: func(argv, env []string, socketPath string) error {
				return m.runInteractiveSession(ctx, re, runID, argv, env)
			},
			OnReport: func(r runner.Report) {
				m.onReport(runID, loop.Name, r)
			},
		}
```

Add `StopLoopGraceful` right after the existing `StopLoop`:

```go
// StopLoopGraceful signals the run's workers to finish their current
// iteration and then stop, without cancelling the run's context — unlike
// StopLoop, an in-flight step is not interrupted. The run's final status
// becomes "done" once every worker returns (a graceful stop is a normal
// end of run, not an error or cancellation). Calling it more than once for
// the same run is a no-op.
func (m *Manager) StopLoopGraceful(runID string) error {
	m.mu.Lock()
	re, ok := m.runs[runID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such run %q", runID)
	}
	re.gracefulOnce.Do(func() { close(re.graceful) })
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./daemon/... -run TestManager -v`
Expected: PASS (all existing `TestManager_*` tests still pass — they don't exercise the new fields, just needed them to compile).

- [ ] **Step 5: Write a new test for the graceful path**

Add to `daemon/manager_test.go`:

```go
func TestManager_StopLoopGracefulFinishesCurrentIterationThenStops(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:  "l",
		Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	path := writeLoopFile(t, dir, loop)

	m := newTestManager(t)
	ch, unsub := m.Subscribe("")
	defer unsub()

	runID, err := m.StartLoop("", path, filepath.Join(dir, ".looper"), dir, 0)
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}

	// Wait for iteration 1's outcome, then request a graceful stop.
	for {
		u := recvUpdate(t, ch)
		if u.Kind == "outcome" {
			break
		}
	}
	if err := m.StopLoopGraceful(runID); err != nil {
		t.Fatalf("StopLoopGraceful: %v", err)
	}

	updates := drainUntilRunDone(t, ch)
	last := updates[len(updates)-1]
	if last.State != "done" {
		t.Errorf("final state = %q, want %q (graceful stop is a normal completion, not stopped/error)", last.State, "done")
	}
}
```

Add `"github.com/jbofill10/looper/config"` to imports if not already present (it is — `writeLoopFile` already uses `*config.Loop`).

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./daemon/... -run TestManager_StopLoopGraceful -v`
Expected: PASS

- [ ] **Step 7: Run the full daemon package test suite**

Run: `go test ./daemon/...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add daemon/manager.go daemon/manager_test.go
git commit -m "feat(daemon): track run base_dir/workdir; add StopLoopGraceful"
```

---

### Task 5: Manager catalog operations — ListLoops, SetLoopEnabled, RunLoopOnce, RenameLoop, DeleteLoop, AutoResume

**Files:**
- Create: `daemon/catalog.go`
- Test: `daemon/catalog_test.go`

**Interfaces:**
- Consumes: `Manager.StartLoop`, `Manager.StopLoopGraceful` (Task 4), `config.LoadLoopLenient` (Task 2), `loadRegistry`/`saveRegistry`/`registryKey`/`registryEntry` (Task 3).
- Produces (consumed by Task 6's RPC handlers):
  - `type LoopSummary struct { Name, Path string; Enabled bool; Steps []string; RunID string }`
  - `func (m *Manager) ListLoops(baseDir string) ([]LoopSummary, error)`
  - `func (m *Manager) SetLoopEnabled(loopName, baseDir, workdir string, enabled bool) (runID string, err error)`
  - `func (m *Manager) RunLoopOnce(loopName, loopFile, baseDir, workdir string) (string, error)`
  - `func (m *Manager) RenameLoop(loopName, newName, baseDir string) error`
  - `func (m *Manager) DeleteLoop(loopName, baseDir string) error`
  - `func (m *Manager) AutoResume() []error`

- [ ] **Step 1: Write the failing tests**

Create `daemon/catalog_test.go`:

```go
package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jbofill10/looper/config"
)

func writeLoopsDir(t *testing.T, projectDir string, loops ...*config.Loop) string {
	t.Helper()
	loopsDir := filepath.Join(projectDir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir loops dir: %v", err)
	}
	for _, l := range loops {
		writeLoopFile(t, loopsDir, l)
	}
	return filepath.Join(projectDir, ".looper")
}

func TestManager_ListLoopsReportsEnabledAndRunningState(t *testing.T) {
	dir := t.TempDir()
	baseDir := writeLoopsDir(t, dir,
		&config.Loop{Name: "a", Steps: []config.Step{{Name: "s1", Type: config.StepScript, Run: "true"}}},
		&config.Loop{Name: "b", Steps: []config.Step{{Name: "s2", Type: config.StepScript, Run: "true"}}},
	)

	m := newTestManager(t)
	if _, err := m.SetLoopEnabled("a", baseDir, dir, true); err != nil {
		t.Fatalf("SetLoopEnabled: %v", err)
	}

	summaries, err := m.ListLoops(baseDir)
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %v, want 2 entries", summaries)
	}
	// sorted by name: a, b
	if !summaries[0].Enabled || summaries[0].RunID == "" {
		t.Errorf("loop a = %+v, want enabled with a run id", summaries[0])
	}
	if summaries[1].Enabled || summaries[1].RunID != "" {
		t.Errorf("loop b = %+v, want disabled with no run id", summaries[1])
	}
	if got := summaries[0].Steps; len(got) != 1 || got[0] != "s1" {
		t.Errorf("loop a steps = %v, want [s1]", got)
	}
}

func TestManager_SetLoopEnabledFalseStopsRunGracefully(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{Name: "a", Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}}}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	ch, unsub := m.Subscribe("")
	defer unsub()

	runID, err := m.SetLoopEnabled("a", baseDir, dir, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	for {
		u := recvUpdate(t, ch)
		if u.Kind == "outcome" {
			break
		}
	}

	if _, err := m.SetLoopEnabled("a", baseDir, dir, false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	updates := drainUntilRunDone(t, ch)
	last := updates[len(updates)-1]
	if last.RunID != runID {
		t.Fatalf("run_done for %q, want %q", last.RunID, runID)
	}

	summaries, err := m.ListLoops(baseDir)
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	if summaries[0].Enabled {
		t.Errorf("loop a still enabled after disable")
	}
}

func TestManager_RunLoopOnceForcesMaxIterationsOne(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{Name: "a", MaxIterations: 0, Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}}}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	ch, unsub := m.Subscribe("")
	defer unsub()

	if _, err := m.RunLoopOnce("a", "", baseDir, dir); err != nil {
		t.Fatalf("RunLoopOnce: %v", err)
	}

	iterations := 0
	for _, u := range drainUntilRunDone(t, ch) {
		if u.Kind == "iteration" {
			iterations++
		}
	}
	if iterations != 1 {
		t.Errorf("iterations = %d, want 1", iterations)
	}

	summaries, err := m.ListLoops(baseDir)
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	if summaries[0].Enabled {
		t.Errorf("RunLoopOnce must not enable the loop")
	}
}

func TestManager_RenameLoopRejectedWhileRunning(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{Name: "a", Steps: []config.Step{{Name: "s", Type: config.StepInteractive, Prompt: "p"}}}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	if _, err := m.SetLoopEnabled("a", baseDir, dir, true); err != nil {
		t.Fatalf("enable: %v", err)
	}

	if err := m.RenameLoop("a", "b", baseDir); err == nil {
		t.Errorf("RenameLoop succeeded while running, want error")
	}
}

func TestManager_DeleteLoopRemovesFileAndRegistryEntry(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{Name: "a", Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}}}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	if err := m.DeleteLoop("a", baseDir); err != nil {
		t.Fatalf("DeleteLoop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "loops", "a.yaml")); !os.IsNotExist(err) {
		t.Errorf("loop file still exists after delete")
	}
	summaries, err := m.ListLoops(baseDir)
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("summaries = %v, want empty after delete", summaries)
	}
}

func TestManager_AutoResumeStartsEnabledLoops(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{Name: "a", Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}}}
	baseDir := writeLoopsDir(t, dir, loop)

	registryPath := filepath.Join(t.TempDir(), "state.json")
	seed := NewManager(nil, "looper")
	seed.SetRegistryPath(registryPath)
	if _, err := seed.SetLoopEnabled("a", baseDir, dir, true); err != nil {
		t.Fatalf("seed enable: %v", err)
	}

	m := NewManager(nil, "looper")
	m.SetRegistryPath(registryPath)
	ch, unsub := m.Subscribe("")
	defer unsub()

	if errs := m.AutoResume(); len(errs) != 0 {
		t.Fatalf("AutoResume errors: %v", errs)
	}

	updates := drainUntilRunDone(t, ch)
	if len(updates) == 0 {
		t.Fatalf("AutoResume did not start the enabled loop")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./daemon/... -run TestManager_ListLoops -v`
Expected: FAIL — `ListLoops` and friends undefined.

- [ ] **Step 3: Implement `daemon/catalog.go`**

```go
// Package daemon: catalog.go implements the Loops-catalog operations the
// TUI's Loops tree drives: listing every configured loop alongside its
// enabled/running state, toggling enabled (persisted via the registry),
// running once, and rename/delete.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jbofill10/looper/config"
)

// LoopSummary is a point-in-time view of one configured loop, as returned
// by ListLoops.
type LoopSummary struct {
	Name    string
	Path    string
	Enabled bool
	Steps   []string
	// RunID is the active run id if this loop currently has one running in
	// this baseDir, else empty.
	RunID string
}

// ListLoops scans <baseDir>/loops/*.yaml and cross-references the
// registry (enabled flag) and active runs (RunID), sorted by loop name. A
// missing loops directory returns an empty slice, not an error.
func (m *Manager) ListLoops(baseDir string) ([]LoopSummary, error) {
	loopsDir := filepath.Join(baseDir, "loops")
	entries, err := os.ReadDir(loopsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading loops directory: %w", err)
	}

	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	activeByLoop := map[string]string{}
	for id, re := range m.runs {
		if re.baseDir == baseDir && re.info.Status == "running" {
			activeByLoop[re.info.LoopName] = id
		}
	}
	m.mu.Unlock()

	var out []LoopSummary
	for _, e := range entries {
		if e.IsDir() || !isYAMLFile(e.Name()) {
			continue
		}
		path := filepath.Join(loopsDir, e.Name())
		loop, err := config.LoadLoopLenient(path)
		if err != nil {
			continue // unreadable/unparseable file: skip rather than fail the whole listing
		}
		stepNames := make([]string, len(loop.Steps))
		for i, s := range loop.Steps {
			stepNames[i] = s.Name
		}
		out = append(out, LoopSummary{
			Name:    loop.Name,
			Path:    path,
			Enabled: registry[registryKey(baseDir, loop.Name)].Enabled,
			Steps:   stepNames,
			RunID:   activeByLoop[loop.Name],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// isYAMLFile reports whether name has a .yaml or .yml extension.
func isYAMLFile(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// SetLoopEnabled persists loopName's enabled flag (keyed by base_dir) to
// the registry. Enabling a loop with no active run in baseDir starts one
// (via StartLoop) and returns its run id; enabling an already-running loop
// is a no-op returning its existing run id. Disabling a running loop
// triggers a graceful stop (StopLoopGraceful) and returns its run id;
// disabling an already-stopped loop returns "".
func (m *Manager) SetLoopEnabled(loopName, baseDir, workdir string, enabled bool) (string, error) {
	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		return "", err
	}
	key := registryKey(baseDir, loopName)
	registry[key] = registryEntry{BaseDir: baseDir, Workdir: workdir, LoopName: loopName, Enabled: enabled}
	if err := saveRegistry(m.registryPath, registry); err != nil {
		return "", err
	}

	runID := m.activeRun(baseDir, loopName)

	if enabled {
		if runID != "" {
			return runID, nil
		}
		return m.StartLoop(loopName, "", baseDir, workdir, 0)
	}
	if runID == "" {
		return "", nil
	}
	return runID, m.StopLoopGraceful(runID)
}

// activeRun returns the run id of loopName's active run in baseDir, or ""
// if it has none.
func (m *Manager) activeRun(baseDir, loopName string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, re := range m.runs {
		if re.baseDir == baseDir && re.info.LoopName == loopName && re.info.Status == "running" {
			return id
		}
	}
	return ""
}

// RunLoopOnce starts loopName as a one-off run with max_iterations forced
// to 1, independent of the loop file's own configured value. It does not
// touch the registry or the loop's enabled flag.
func (m *Manager) RunLoopOnce(loopName, loopFile, baseDir, workdir string) (string, error) {
	path := loopFile
	if path == "" {
		path = filepath.Join(baseDir, "loops", loopName+".yaml")
	}
	loop, err := config.LoadLoop(path)
	if err != nil {
		return "", err
	}
	once := *loop
	once.MaxIterations = 1

	tmp, err := os.MkdirTemp("", "looper-run-once-*")
	if err != nil {
		return "", fmt.Errorf("preparing run-once loop file: %w", err)
	}
	oncePath := filepath.Join(tmp, loopName+".yaml")
	if err := config.SaveLoop(&once, oncePath); err != nil {
		return "", fmt.Errorf("writing run-once loop file: %w", err)
	}
	return m.StartLoop("", oncePath, baseDir, workdir, 0)
}

// RenameLoop renames loopName's YAML file (updating its Name field) and
// its registry entry to newName. It returns an error if the loop
// currently has an active run in baseDir.
func (m *Manager) RenameLoop(loopName, newName, baseDir string) error {
	if runID := m.activeRun(baseDir, loopName); runID != "" {
		return fmt.Errorf("loop %q has an active run (%s); stop it before renaming", loopName, runID)
	}

	oldPath := filepath.Join(baseDir, "loops", loopName+".yaml")
	loop, err := config.LoadLoopLenient(oldPath)
	if err != nil {
		return err
	}
	loop.Name = newName
	newPath := filepath.Join(baseDir, "loops", newName+".yaml")
	if err := config.SaveLoop(loop, newPath); err != nil {
		return err
	}
	if err := os.Remove(oldPath); err != nil {
		return fmt.Errorf("removing old loop file: %w", err)
	}

	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		return err
	}
	oldKey := registryKey(baseDir, loopName)
	if entry, ok := registry[oldKey]; ok {
		delete(registry, oldKey)
		entry.LoopName = newName
		registry[registryKey(baseDir, newName)] = entry
		if err := saveRegistry(m.registryPath, registry); err != nil {
			return err
		}
	}
	return nil
}

// DeleteLoop removes loopName's YAML file and its registry entry (if any).
// It returns an error if the loop currently has an active run in baseDir.
func (m *Manager) DeleteLoop(loopName, baseDir string) error {
	if runID := m.activeRun(baseDir, loopName); runID != "" {
		return fmt.Errorf("loop %q has an active run (%s); stop it before deleting", loopName, runID)
	}

	path := filepath.Join(baseDir, "loops", loopName+".yaml")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing loop file: %w", err)
	}

	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		return err
	}
	key := registryKey(baseDir, loopName)
	if _, ok := registry[key]; ok {
		delete(registry, key)
		if err := saveRegistry(m.registryPath, registry); err != nil {
			return err
		}
	}
	return nil
}

// AutoResume starts every registry entry marked enabled, using its
// persisted base_dir/workdir. Called once at daemon startup. Errors (e.g.
// a loop file since deleted) are collected and returned rather than
// aborting the rest — one bad entry must not block every other loop from
// resuming.
func (m *Manager) AutoResume() []error {
	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, entry := range registry {
		if !entry.Enabled {
			continue
		}
		if _, err := m.StartLoop(entry.LoopName, "", entry.BaseDir, entry.Workdir, 0); err != nil {
			errs = append(errs, fmt.Errorf("auto-resume %q: %w", entry.LoopName, err))
		}
	}
	return errs
}
```

- [ ] **Step 4: Run the new tests**

Run: `go test ./daemon/... -run TestManager_ListLoops -v`
Run: `go test ./daemon/... -run TestManager_SetLoopEnabled -v`
Run: `go test ./daemon/... -run TestManager_RunLoopOnce -v`
Run: `go test ./daemon/... -run TestManager_RenameLoop -v`
Run: `go test ./daemon/... -run TestManager_DeleteLoop -v`
Run: `go test ./daemon/... -run TestManager_AutoResume -v`
Expected: PASS for all.

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./daemon/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add daemon/catalog.go daemon/catalog_test.go
git commit -m "feat(daemon): add Loops-catalog operations (list/enable/run-once/rename/delete/auto-resume)"
```

---

### Task 6: Proto messages/RPCs + service handlers

**Files:**
- Modify: `proto/looper.proto`
- Generated (via script, not hand-edited): `rpc/looper.pb.go`, `rpc/looper_grpc.pb.go`
- Modify: `daemon/service.go`
- Test: `daemon/service_test.go`

**Interfaces:**
- Consumes: `Manager.ListLoops/SetLoopEnabled/RunLoopOnce/StopLoopGraceful/RenameLoop/DeleteLoop` (Tasks 4–5).
- Produces (consumed by Task 8's TUI wiring): the six new RPCs on `rpc.LooperClient`.

- [ ] **Step 1: Confirm protoc tooling is available**

Run: `command -v protoc protoc-gen-go protoc-gen-go-grpc`
Expected: all three print a path. If any is missing, stop and report to the user — this task cannot proceed without them (installing protoc is outside this plan's scope; do not attempt a manual hand-written substitute for generated pb.go code).

- [ ] **Step 2: Add the new messages and RPCs to `proto/looper.proto`**

Add these RPC lines inside the existing `service Looper { ... }` block, after `RespondDecision`:

```proto
  // ListLoops lists every loop configured under base_dir, with its
  // enabled/running state.
  rpc ListLoops(ListLoopsRequest) returns (ListLoopsResponse);
  // SetLoopEnabled persists a loop's enabled flag and starts/gracefully
  // stops its run accordingly.
  rpc SetLoopEnabled(SetLoopEnabledRequest) returns (SetLoopEnabledResponse);
  // RunLoopOnce starts a loop as a one-off run (max_iterations forced to 1).
  rpc RunLoopOnce(RunLoopOnceRequest) returns (RunLoopOnceResponse);
  // StopLoopGraceful lets a run's in-flight iteration finish, then stops it
  // before the next iteration (unlike StopLoop, does not cancel mid-step).
  rpc StopLoopGraceful(StopLoopGracefulRequest) returns (StopLoopGracefulResponse);
  // RenameLoop renames a loop's file and registry entry.
  rpc RenameLoop(RenameLoopRequest) returns (RenameLoopResponse);
  // DeleteLoop deletes a loop's file and registry entry.
  rpc DeleteLoop(DeleteLoopRequest) returns (DeleteLoopResponse);
```

Add these messages after `RespondDecisionResponse {}`:

```proto
message ListLoopsRequest { string base_dir = 1; }
message LoopInfo {
  string name = 1;
  string path = 2;
  bool enabled = 3;
  repeated string steps = 4;
  string run_id = 5; // active run id, empty if not running
}
message ListLoopsResponse { repeated LoopInfo loops = 1; }

message SetLoopEnabledRequest {
  string loop_name = 1;
  string base_dir = 2;
  string workdir = 3;
  bool enabled = 4;
}
message SetLoopEnabledResponse { string run_id = 1; } // empty if disabling an already-stopped loop

message RunLoopOnceRequest {
  string loop_name = 1;
  string loop_file = 2;
  string base_dir = 3;
  string workdir = 4;
}
message RunLoopOnceResponse { string run_id = 1; }

message StopLoopGracefulRequest { string run_id = 1; }
message StopLoopGracefulResponse {}

message RenameLoopRequest {
  string loop_name = 1;
  string new_name = 2;
  string base_dir = 3;
}
message RenameLoopResponse {}

message DeleteLoopRequest {
  string loop_name = 1;
  string base_dir = 2;
}
message DeleteLoopResponse {}
```

- [ ] **Step 3: Regenerate the Go gRPC code**

Run: `./scripts/gen-proto.sh`
Expected: prints `generated rpc/*.pb.go`; `git status` shows `rpc/looper.pb.go` and `rpc/looper_grpc.pb.go` modified.

- [ ] **Step 4: Confirm it builds**

Run: `go build ./...`
Expected: succeeds (the new RPC methods exist on `rpc.LooperClient`/`rpc.LooperServer`, unimplemented server-side yet via `UnimplementedLooperServer`).

- [ ] **Step 5: Write the failing service tests**

Add to `daemon/service_test.go` (mirroring the file's existing `startTestServer`/`writeLoopYAML`/`dial` helpers):

```go
func TestService_ListLoopsAndSetLoopEnabled(t *testing.T) {
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLoopYAML(t, loopsDir, "a", map[string]any{
		"steps": []map[string]any{{"name": "s", "type": "script", "run": "true"}},
	})

	c := startTestServer(t)
	ctx := context.Background()

	listResp, err := c.ListLoops(ctx, &rpc.ListLoopsRequest{BaseDir: filepath.Join(dir, ".looper")})
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	if len(listResp.Loops) != 1 || listResp.Loops[0].Enabled {
		t.Fatalf("loops = %v, want one disabled loop", listResp.Loops)
	}

	setResp, err := c.SetLoopEnabled(ctx, &rpc.SetLoopEnabledRequest{
		LoopName: "a", BaseDir: filepath.Join(dir, ".looper"), Workdir: dir, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SetLoopEnabled: %v", err)
	}
	if setResp.RunId == "" {
		t.Fatalf("SetLoopEnabled did not return a run id")
	}

	listResp, err = c.ListLoops(ctx, &rpc.ListLoopsRequest{BaseDir: filepath.Join(dir, ".looper")})
	if err != nil {
		t.Fatalf("ListLoops after enable: %v", err)
	}
	if !listResp.Loops[0].Enabled || listResp.Loops[0].RunId != setResp.RunId {
		t.Errorf("loops after enable = %v, want enabled with run id %q", listResp.Loops, setResp.RunId)
	}
}

func TestService_RunLoopOnce(t *testing.T) {
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLoopYAML(t, loopsDir, "a", map[string]any{
		"steps": []map[string]any{{"name": "s", "type": "script", "run": "true"}},
	})

	c := startTestServer(t)
	ctx := context.Background()
	resp, err := c.RunLoopOnce(ctx, &rpc.RunLoopOnceRequest{LoopName: "a", BaseDir: filepath.Join(dir, ".looper"), Workdir: dir})
	if err != nil {
		t.Fatalf("RunLoopOnce: %v", err)
	}
	if resp.RunId == "" {
		t.Errorf("RunLoopOnce did not return a run id")
	}
}

func TestService_StopLoopGraceful(t *testing.T) {
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLoopYAML(t, loopsDir, "a", map[string]any{
		"steps": []map[string]any{{"name": "s", "type": "script", "run": "true"}},
	})

	c := startTestServer(t)
	ctx := context.Background()
	startResp, err := c.SetLoopEnabled(ctx, &rpc.SetLoopEnabledRequest{
		LoopName: "a", BaseDir: filepath.Join(dir, ".looper"), Workdir: dir, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SetLoopEnabled: %v", err)
	}
	if _, err := c.StopLoopGraceful(ctx, &rpc.StopLoopGracefulRequest{RunId: startResp.RunId}); err != nil {
		t.Fatalf("StopLoopGraceful: %v", err)
	}
}

func TestService_RenameAndDeleteLoop(t *testing.T) {
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLoopYAML(t, loopsDir, "a", map[string]any{
		"steps": []map[string]any{{"name": "s", "type": "script", "run": "true"}},
	})

	c := startTestServer(t)
	ctx := context.Background()
	if _, err := c.RenameLoop(ctx, &rpc.RenameLoopRequest{LoopName: "a", NewName: "b", BaseDir: filepath.Join(dir, ".looper")}); err != nil {
		t.Fatalf("RenameLoop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(loopsDir, "b.yaml")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}

	if _, err := c.DeleteLoop(ctx, &rpc.DeleteLoopRequest{LoopName: "b", BaseDir: filepath.Join(dir, ".looper")}); err != nil {
		t.Fatalf("DeleteLoop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(loopsDir, "b.yaml")); !os.IsNotExist(err) {
		t.Errorf("deleted file still exists")
	}
}
```

Note: `startTestServer` builds `daemon.New()`, whose `Manager` uses the real `defaultRegistryPath()`. Before this test file's tests run, add `t.Setenv("XDG_RUNTIME_DIR", t.TempDir())` as the first line of each of the four new test functions above, so each gets an isolated registry file instead of touching the developer's real one.

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./daemon/... -run TestService_ListLoops -v`
Expected: FAIL — `s.ListLoops` etc. not implemented (returns `Unimplemented` from the embedded `UnimplementedLooperServer`).

- [ ] **Step 7: Implement the handlers in `daemon/service.go`**

Add after the existing `RespondDecision` handler:

```go
// ListLoops lists every loop configured under req.BaseDir.
func (s *Server) ListLoops(ctx context.Context, req *rpc.ListLoopsRequest) (*rpc.ListLoopsResponse, error) {
	summaries, err := s.manager.ListLoops(req.GetBaseDir())
	if err != nil {
		return nil, err
	}
	out := make([]*rpc.LoopInfo, len(summaries))
	for i, l := range summaries {
		out[i] = &rpc.LoopInfo{Name: l.Name, Path: l.Path, Enabled: l.Enabled, Steps: l.Steps, RunId: l.RunID}
	}
	return &rpc.ListLoopsResponse{Loops: out}, nil
}

// SetLoopEnabled persists a loop's enabled flag and starts/gracefully
// stops its run accordingly.
func (s *Server) SetLoopEnabled(ctx context.Context, req *rpc.SetLoopEnabledRequest) (*rpc.SetLoopEnabledResponse, error) {
	runID, err := s.manager.SetLoopEnabled(req.GetLoopName(), req.GetBaseDir(), req.GetWorkdir(), req.GetEnabled())
	if err != nil {
		return nil, err
	}
	return &rpc.SetLoopEnabledResponse{RunId: runID}, nil
}

// RunLoopOnce starts a loop as a one-off run.
func (s *Server) RunLoopOnce(ctx context.Context, req *rpc.RunLoopOnceRequest) (*rpc.RunLoopOnceResponse, error) {
	runID, err := s.manager.RunLoopOnce(req.GetLoopName(), req.GetLoopFile(), req.GetBaseDir(), req.GetWorkdir())
	if err != nil {
		return nil, err
	}
	return &rpc.RunLoopOnceResponse{RunId: runID}, nil
}

// StopLoopGraceful lets a run's in-flight iteration finish, then stops it.
func (s *Server) StopLoopGraceful(ctx context.Context, req *rpc.StopLoopGracefulRequest) (*rpc.StopLoopGracefulResponse, error) {
	if err := s.manager.StopLoopGraceful(req.GetRunId()); err != nil {
		return nil, err
	}
	return &rpc.StopLoopGracefulResponse{}, nil
}

// RenameLoop renames a loop's file and registry entry.
func (s *Server) RenameLoop(ctx context.Context, req *rpc.RenameLoopRequest) (*rpc.RenameLoopResponse, error) {
	if err := s.manager.RenameLoop(req.GetLoopName(), req.GetNewName(), req.GetBaseDir()); err != nil {
		return nil, err
	}
	return &rpc.RenameLoopResponse{}, nil
}

// DeleteLoop deletes a loop's file and registry entry.
func (s *Server) DeleteLoop(ctx context.Context, req *rpc.DeleteLoopRequest) (*rpc.DeleteLoopResponse, error) {
	if err := s.manager.DeleteLoop(req.GetLoopName(), req.GetBaseDir()); err != nil {
		return nil, err
	}
	return &rpc.DeleteLoopResponse{}, nil
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./daemon/... -run TestService_ListLoops -v`
Run: `go test ./daemon/... -run TestService_RunLoopOnce -v`
Run: `go test ./daemon/... -run TestService_StopLoopGraceful -v`
Run: `go test ./daemon/... -run TestService_RenameAndDeleteLoop -v`
Expected: PASS for all.

- [ ] **Step 9: Run the full daemon package test suite, then the whole repo**

Run: `go test ./daemon/...`
Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add proto/looper.proto rpc/looper.pb.go rpc/looper_grpc.pb.go daemon/service.go daemon/service_test.go
git commit -m "feat(daemon): expose ListLoops/SetLoopEnabled/RunLoopOnce/StopLoopGraceful/RenameLoop/DeleteLoop RPCs"
```

---

### Task 7: Wire AutoResume into `looper daemon` startup

**Files:**
- Modify: `daemon/daemon.go`
- Modify: `cli/daemon.go`
- Test: `daemon/daemon_test.go`

**Interfaces:**
- Produces: `func (s *Server) AutoResume() []error` (delegates to `s.manager.AutoResume()`).

- [ ] **Step 1: Write the failing test**

Add to `daemon/daemon_test.go` (matching its existing style — check the file first with `sed -n '1,40p' daemon/daemon_test.go`):

```go
func TestServer_AutoResumeDelegatesToManager(t *testing.T) {
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLoopFile(t, loopsDir, &config.Loop{
		Name: "a", Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	})

	s := New()
	s.manager.SetRegistryPath(filepath.Join(t.TempDir(), "state.json"))
	if _, err := s.manager.SetLoopEnabled("a", filepath.Join(dir, ".looper"), dir, true); err != nil {
		t.Fatalf("seed enable: %v", err)
	}
	// Simulate a fresh daemon process picking the same registry back up.
	s2 := New()
	s2.manager.SetRegistryPath(s.manager.registryPath)
	if errs := s2.AutoResume(); len(errs) != 0 {
		t.Fatalf("AutoResume errors: %v", errs)
	}
}
```

Add `"os"`, `"path/filepath"`, `"github.com/jbofill10/looper/config"` to imports if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./daemon/... -run TestServer_AutoResume -v`
Expected: FAIL — `Server` has no method `AutoResume`.

- [ ] **Step 3: Add the method to `daemon/daemon.go`**

Add near `Stop`:

```go
// AutoResume starts every registry entry marked enabled. Called once by
// the `looper daemon` command right before Serve, so a freshly (re)started
// daemon picks back up whatever loops were enabled before it last stopped.
func (s *Server) AutoResume() []error {
	return s.manager.AutoResume()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./daemon/... -run TestServer_AutoResume -v`
Expected: PASS

- [ ] **Step 5: Call it from `cli/daemon.go`**

In `newDaemonCmd`'s `RunE`, right after `srv := daemon.New()`:

```go
			srv := daemon.New()
			for _, err := range srv.AutoResume() {
				fmt.Fprintf(cmd.ErrOrStderr(), "auto-resume: %v\n", err)
			}
```

- [ ] **Step 6: Run the cli package tests**

Run: `go test ./cli/...`
Expected: PASS (no existing test asserts on `looper daemon`'s exact startup sequence beyond it serving successfully — confirm with `grep -n "newDaemonCmd\|TestDaemon" cli/daemon_test.go` first; if a test does assert something incompatible, adjust the test to allow for the new stderr lines rather than removing the AutoResume call).

- [ ] **Step 7: Full build + test**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add daemon/daemon.go daemon/daemon_test.go cli/daemon.go
git commit -m "feat(cli): auto-resume enabled loops when looperd starts"
```

---

### Task 8: TUI plumbing — Options, program.go wiring, periodic Loops refresh

**Files:**
- Modify: `tui/model.go` (Options + new msg types only — no view/keybinding logic yet, that's Tasks 9–10)
- Modify: `tui/program.go`
- Test: `tui/program_test.go`

**Interfaces:**
- Produces on `tui.Options`:
  - `ListLoopsFn func() tea.Cmd` — returns a `tea.Cmd` yielding `LoopsSnapshotMsg` or `ErrMsg`.
  - `SetLoopEnabledFn func(loopName string, enabled bool) tea.Cmd`
  - `RunLoopOnceFn func(loopName string) tea.Cmd`
  - `StopLoopGracefulFn func(runID string) tea.Cmd`
  - `RenameLoopFn func(loopName, newName string) tea.Cmd`
  - `DeleteLoopFn func(loopName string) tea.Cmd`
- Produces on `tui` package:
  - `type LoopSnapshot struct { Name, Path string; Enabled bool; Steps []string; RunID string }`
  - `type LoopsSnapshotMsg []LoopSnapshot`
  - `type loopsTickMsg struct{}` (unexported — drives periodic refresh)
- Consumes: the six new RPCs from Task 6.

- [ ] **Step 1: Add the new Options fields and message types to `tui/model.go`**

Add to `Options` (after `AuthorFn`):

```go
	// ListLoopsFn, if set, fetches the current Loops-catalog snapshot. It
	// returns a tea.Cmd yielding a LoopsSnapshotMsg or ErrMsg.
	ListLoopsFn func() tea.Cmd
	// SetLoopEnabledFn toggles a loop's enabled state.
	SetLoopEnabledFn func(loopName string, enabled bool) tea.Cmd
	// RunLoopOnceFn starts a loop as a one-off run.
	RunLoopOnceFn func(loopName string) tea.Cmd
	// StopLoopGracefulFn lets a run finish its current iteration, then stops it.
	StopLoopGracefulFn func(runID string) tea.Cmd
	// RenameLoopFn renames a loop.
	RenameLoopFn func(loopName, newName string) tea.Cmd
	// DeleteLoopFn deletes a loop.
	DeleteLoopFn func(loopName string) tea.Cmd
```

Add new top-level types (near `RunSnapshot`):

```go
// LoopSnapshot is a point-in-time view of one configured loop, as returned
// by the daemon's ListLoops RPC.
type LoopSnapshot struct {
	Name    string
	Path    string
	Enabled bool
	Steps   []string
	RunID   string
}

// LoopsSnapshotMsg carries the current Loops-catalog snapshot, sent
// periodically by the program wiring (see tui.Run) so the Loops section
// stays in sync with daemon-side enable/run-once/rename/delete actions
// taken from other clients.
type LoopsSnapshotMsg []LoopSnapshot
```

- [ ] **Step 2: Write the failing test for the program-wiring translation helpers**

`tui/program_test.go` already tests pure translation helpers like `updateFromProto`/`runsSnapshotFromProto` — add a matching one. First read the existing file to match its style:

Run: `sed -n '1,40p' tui/program_test.go`

Then add:

```go
func TestLoopsSnapshotFromProto(t *testing.T) {
	resp := []*rpc.LoopInfo{
		{Name: "a", Path: "/x/a.yaml", Enabled: true, Steps: []string{"s1", "s2"}, RunId: "run-001"},
	}
	got := loopsSnapshotFromProto(resp)
	want := LoopsSnapshotMsg{{Name: "a", Path: "/x/a.yaml", Enabled: true, Steps: []string{"s1", "s2"}, RunID: "run-001"}}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("loopsSnapshotFromProto = %+v, want %+v", got, want)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./tui/... -run TestLoopsSnapshotFromProto -v`
Expected: FAIL — `loopsSnapshotFromProto` undefined.

- [ ] **Step 4: Implement the wiring in `tui/program.go`**

Add the translation helper near `runsSnapshotFromProto`:

```go
// loopsSnapshotFromProto translates a ListLoops response's []*rpc.LoopInfo
// into the pure LoopsSnapshotMsg the Model understands.
func loopsSnapshotFromProto(loops []*rpc.LoopInfo) LoopsSnapshotMsg {
	out := make(LoopsSnapshotMsg, 0, len(loops))
	for _, l := range loops {
		out = append(out, LoopSnapshot{
			Name: l.GetName(), Path: l.GetPath(), Enabled: l.GetEnabled(),
			Steps: l.GetSteps(), RunID: l.GetRunId(),
		})
	}
	return out
}
```

Add a `loopsTickInterval` constant near `rpcTimeout`:

```go
// loopsTickInterval bounds how often the program wiring re-fetches the
// Loops-catalog snapshot: there is no push stream for it (unlike
// StreamState for runs), so it's polled.
const loopsTickInterval = 2 * time.Second
```

Add the six `*Fn` builders, following the existing `respondFn`/`attachFn` pattern:

```go
// listLoopsFn returns the Options.ListLoopsFn implementation.
func listLoopsFn(ctx context.Context, cl rpc.LooperClient, baseDir string) func() tea.Cmd {
	return func() tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			resp, err := cl.ListLoops(rctx, &rpc.ListLoopsRequest{BaseDir: baseDir})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return loopsSnapshotFromProto(resp.GetLoops())
		}
	}
}

// setLoopEnabledFn returns the Options.SetLoopEnabledFn implementation.
func setLoopEnabledFn(ctx context.Context, cl rpc.LooperClient, baseDir, workdir string) func(string, bool) tea.Cmd {
	return func(loopName string, enabled bool) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.SetLoopEnabled(rctx, &rpc.SetLoopEnabledRequest{
				LoopName: loopName, BaseDir: baseDir, Workdir: workdir, Enabled: enabled,
			})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()
		}
	}
}

// runLoopOnceFn returns the Options.RunLoopOnceFn implementation.
func runLoopOnceFn(ctx context.Context, cl rpc.LooperClient, baseDir, workdir string) func(string) tea.Cmd {
	return func(loopName string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.RunLoopOnce(rctx, &rpc.RunLoopOnceRequest{LoopName: loopName, BaseDir: baseDir, Workdir: workdir})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()
		}
	}
}

// stopLoopGracefulFn returns the Options.StopLoopGracefulFn implementation.
func stopLoopGracefulFn(ctx context.Context, cl rpc.LooperClient, baseDir string) func(string) tea.Cmd {
	return func(runID string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.StopLoopGraceful(rctx, &rpc.StopLoopGracefulRequest{RunId: runID})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()
		}
	}
}

// renameLoopFn returns the Options.RenameLoopFn implementation.
func renameLoopFn(ctx context.Context, cl rpc.LooperClient, baseDir string) func(string, string) tea.Cmd {
	return func(loopName, newName string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.RenameLoop(rctx, &rpc.RenameLoopRequest{LoopName: loopName, NewName: newName, BaseDir: baseDir})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()
		}
	}
}

// deleteLoopFn returns the Options.DeleteLoopFn implementation.
func deleteLoopFn(ctx context.Context, cl rpc.LooperClient, baseDir string) func(string) tea.Cmd {
	return func(loopName string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.DeleteLoop(rctx, &rpc.DeleteLoopRequest{LoopName: loopName, BaseDir: baseDir})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()
		}
	}
}
```

Wire them into `Run`'s `NewModel(Options{...})` call (add fields; `wd` and the `.looper` base dir are already computed as `wd`/`filepath.Join(wd, ".looper")`):

```go
	baseDir := filepath.Join(wd, ".looper")
	var p *tea.Program
	model := NewModel(Options{
		RespondFn:          respondFn(ctx, cl),
		AttachFn:           attachFn(ctx, cl, &p),
		ProjectDir:         wd,
		NewLoopPathFn:      newLoopPathFn(wd),
		AuthorFn:           authorFn(&p, global, wd),
		ListLoopsFn:        listLoopsFn(ctx, cl, baseDir),
		SetLoopEnabledFn:   setLoopEnabledFn(ctx, cl, baseDir, wd),
		RunLoopOnceFn:      runLoopOnceFn(ctx, cl, baseDir, wd),
		StopLoopGracefulFn: stopLoopGracefulFn(ctx, cl, baseDir),
		RenameLoopFn:       renameLoopFn(ctx, cl, baseDir),
		DeleteLoopFn:       deleteLoopFn(ctx, cl, baseDir),
	})
	p = tea.NewProgram(model)

	go sendRunsSnapshot(ctx, p, cl)
	go streamUpdates(ctx, p, cl)
	go pollLoopsSnapshot(ctx, p, cl, baseDir)
```

Add `pollLoopsSnapshot` near `sendRunsSnapshot`:

```go
// pollLoopsSnapshot periodically fetches the Loops-catalog snapshot and
// delivers it to p as a LoopsSnapshotMsg. There is no push stream for
// catalog state (unlike StreamState for runs), so it's polled on
// loopsTickInterval until ctx is cancelled.
func pollLoopsSnapshot(ctx context.Context, p *tea.Program, cl rpc.LooperClient, baseDir string) {
	fetch := listLoopsFn(ctx, cl, baseDir)
	ticker := time.NewTicker(loopsTickInterval)
	defer ticker.Stop()
	p.Send(fetch()())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Send(fetch()())
		}
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./tui/... -run TestLoopsSnapshotFromProto -v`
Expected: PASS

- [ ] **Step 6: Run the full tui package test suite, then the whole repo**

Run: `go test ./tui/...`
Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add tui/model.go tui/program.go tui/program_test.go
git commit -m "feat(tui): wire Loops-catalog RPCs into the program layer"
```

---

### Task 9: Model — Loops tree state, rendering, expand/collapse

**Files:**
- Modify: `tui/model.go`
- Test: `tui/model_test.go`, `tui/view_test.go`

**Design note — two independently navigable lists in one view:** `viewFleet` now shows two sections, the Loops tree and the existing Workers table. They need separate cursors (`treeCursor` for the tree, the pre-existing `cursor` for Workers), and something has to decide which one `up`/`down`/`enter` currently drive. This task adds `Model.loopsFocused bool` and a `tab` key that flips it — **critically, the zero value is `false` = Workers table focused**, so that every existing test (`TestView_CursorMovement`, `TestView_EnterSwitchesToFocus`, `TestView_DecisionKeysInvokeRespondFn`, etc.), none of which ever send a `LoopsSnapshotMsg` or press `tab`, keeps behaving exactly as it does today: `up`/`down` move `m.cursor` and `enter` focuses a worker, unchanged. Pressing `tab` is how you move focus *into* the Loops tree; pressing it again returns focus to Workers.

**Interfaces:**
- Produces:
  - `Model.loops []LoopSnapshot` (from `LoopsSnapshotMsg`)
  - `Model.expandedLoop string` (loop name currently expanded, "" = none)
  - `Model.treeCursor int` (cursor position within the combined Loops-section row list: loop header rows + expanded loop's step rows)
  - `Model.loopsFocused bool` (zero value `false` = Workers table has up/down/enter focus, exactly matching today's behavior; `true` = the Loops tree does; `tab` toggles it)
  - `func (m Model) treeRows() []treeRow` where `type treeRow struct { Kind string /* "loop" | "step" */; LoopName string; StepIndex int }`
- Consumes: Task 8's `LoopsSnapshotMsg`.

- [ ] **Step 1: Write the failing tests**

Add to `tui/model_test.go`:

```go
func TestModel_LoopsSnapshotPopulatesTreeRows(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(LoopsSnapshotMsg{
		{Name: "a", Steps: []string{"s1", "s2"}},
		{Name: "b", Steps: []string{"s3"}},
	})
	m = next.(Model)

	rows := m.treeRows()
	if len(rows) != 2 {
		t.Fatalf("treeRows (collapsed) = %v, want 2 loop rows", rows)
	}
	if rows[0].Kind != "loop" || rows[0].LoopName != "a" {
		t.Errorf("rows[0] = %+v, want loop row for a", rows[0])
	}
}

func TestModel_ExpandingLoopShowsItsSteps(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(LoopsSnapshotMsg{
		{Name: "a", Steps: []string{"s1", "s2"}},
	})
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(Model)

	rows := m.treeRows()
	if len(rows) != 3 {
		t.Fatalf("treeRows (expanded) = %v, want 1 loop row + 2 step rows", rows)
	}
	if rows[1].Kind != "step" || rows[1].LoopName != "a" || rows[1].StepIndex != 0 {
		t.Errorf("rows[1] = %+v, want step row (a, 0)", rows[1])
	}
	if rows[2].Kind != "step" || rows[2].StepIndex != 1 {
		t.Errorf("rows[2] = %+v, want step row (a, 1)", rows[2])
	}
}

func TestModel_UpDownMovesWorkersCursorByDefault(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(LoopsSnapshotMsg{{Name: "a"}, {Name: "b"}})
	m = next.(Model)
	next, _ = m.Update(StateUpdateMsg{RunID: "r1", WorkerID: "w1", Kind: "state"})
	m = next.(Model)
	next, _ = m.Update(StateUpdateMsg{RunID: "r1", WorkerID: "w2", Kind: "state"})
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.cursor != 1 {
		t.Errorf("cursor (Workers) = %d, want 1 (down must move the Workers cursor by default, unchanged from today's behavior)", m.cursor)
	}
	if m.treeCursor != 0 {
		t.Errorf("treeCursor = %d, want unchanged at 0 while Workers has default focus", m.treeCursor)
	}
}

func TestModel_TabSwitchesFocusToLoopsTree(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(LoopsSnapshotMsg{{Name: "a"}, {Name: "b"}})
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if !m.loopsFocused {
		t.Fatalf("tab did not switch focus to the Loops tree")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.treeCursor != 1 {
		t.Errorf("treeCursor = %d, want 1 (down should move the tree cursor once tab has focused it)", m.treeCursor)
	}
	if m.cursor != 0 {
		t.Errorf("cursor (Workers) = %d, want unchanged at 0 while the Loops tree has focus", m.cursor)
	}
}
```

Add to `tui/view_test.go`:

```go
func TestView_FleetShowsLoopsSection(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(LoopsSnapshotMsg{{Name: "jira-tracker", Enabled: true, Steps: []string{"s1"}}})
	m = next.(Model)

	out := m.View()
	if !strings.Contains(out, "Loops") || !strings.Contains(out, "jira-tracker") || !strings.Contains(out, "[on]") {
		t.Errorf("View() = %q, want a Loops section listing jira-tracker as [on]", out)
	}
}
```

(Both test files already import `tea "github.com/charmbracelet/bubbletea"` and `"strings"` — check and add if missing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tui/... -run TestModel_LoopsSnapshot -v`
Run: `go test ./tui/... -run TestModel_ExpandingLoop -v`
Run: `go test ./tui/... -run TestModel_UpDownMovesWorkersCursorByDefault -v`
Run: `go test ./tui/... -run TestModel_TabSwitchesFocusToLoopsTree -v`
Run: `go test ./tui/... -run TestView_FleetShowsLoopsSection -v`
Expected: FAIL — `treeRows`/loops handling undefined; `Update` doesn't recognize `LoopsSnapshotMsg`.

- [ ] **Step 3: Add the fields, message handling, and tree-row computation**

Add to the `Model` struct (near `builderMsg`):

```go
	loops        []LoopSnapshot
	expandedLoop string // "" = no loop expanded
	treeCursor   int    // cursor position within treeRows()
	loopsFocused bool   // false (zero value) = up/down/enter drive the Workers table, exactly as before this task; true = the Loops tree
```

Add a `treeRow` type near `workerRow`:

```go
// treeRow is one row of the Loops section's tree: either a loop header row
// or (when that loop is expanded) one of its step rows.
type treeRow struct {
	Kind      string // "loop" | "step"
	LoopName  string
	StepIndex int // valid only when Kind == "step"
}
```

Add `treeRows` near `Workers`:

```go
// treeRows returns the Loops section's current flattened row list: one
// "loop" row per configured loop (sorted as delivered by ListLoops, i.e.
// by name), plus — for whichever loop is expanded — a "step" row per step
// in order, interleaved right after that loop's row.
func (m Model) treeRows() []treeRow {
	var rows []treeRow
	for _, l := range m.loops {
		rows = append(rows, treeRow{Kind: "loop", LoopName: l.Name})
		if l.Name == m.expandedLoop {
			for i := range l.Steps {
				rows = append(rows, treeRow{Kind: "step", LoopName: l.Name, StepIndex: i})
			}
		}
	}
	return rows
}

// loopByName returns the LoopSnapshot named name, and whether one exists.
func (m Model) loopByName(name string) (LoopSnapshot, bool) {
	for _, l := range m.loops {
		if l.Name == name {
			return l, true
		}
	}
	return LoopSnapshot{}, false
}
```

Handle `LoopsSnapshotMsg` in `Update` (add a case alongside `StateUpdateMsg`/`RunsSnapshotMsg`):

```go
	case LoopsSnapshotMsg:
		m.loops = []LoopSnapshot(msg)
		if rows := m.treeRows(); m.treeCursor >= len(rows) {
			m.treeCursor = len(rows) - 1
		}
		if m.treeCursor < 0 {
			m.treeCursor = 0
		}
		return m, nil
```

Replace `handleKey`'s existing `"up", "k"` / `"down", "j"` / `"enter"` cases (they currently drive only the Workers table via `m.cursor`/`m.Workers()`) so they branch on `m.loopsFocused`, and add `tab` (switch focus) and `" "` (expand/collapse, Loops-tree only). Note the polarity: `m.loopsFocused == false` (the zero value, i.e. every pre-existing test that never presses `tab`) must reproduce today's exact behavior — Workers gets up/down/enter:

```go
	case "up", "k":
		if m.view != viewFleet {
			break
		}
		if m.loopsFocused {
			if m.treeCursor > 0 {
				m.treeCursor--
			}
		} else if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.view != viewFleet {
			break
		}
		if m.loopsFocused {
			if last := len(m.treeRows()) - 1; m.treeCursor < last {
				m.treeCursor++
			}
		} else if last := len(m.Workers()) - 1; m.cursor < last {
			m.cursor++
		}
	case "tab":
		if m.view == viewFleet {
			m.loopsFocused = !m.loopsFocused
		}
	case " ":
		if m.view == viewFleet && m.loopsFocused {
			rows := m.treeRows()
			if m.treeCursor < len(rows) && rows[m.treeCursor].Kind == "loop" {
				name := rows[m.treeCursor].LoopName
				if m.expandedLoop == name {
					m.expandedLoop = ""
				} else {
					m.expandedLoop = name
				}
			}
		}
	case "enter":
		if m.view != viewFleet || m.loopsFocused {
			break
		}
		rows := m.Workers()
		if m.cursor < len(rows) {
			row := rows[m.cursor]
			m.focusRun, m.focusWorker = row.RunID, row.WorkerID
			m.view = viewFocus
		}
```

(This `case "up", "k":`/`"down", "j":`/`"enter":` block replaces the three existing cases of the same names in `handleKey` verbatim — do not leave the old bodies in place alongside these. `enter` now only focuses a worker when the Workers table has focus, matching this task's design note: the Loops tree has no "enter" action of its own yet — `space` is its expand/collapse key, and Task 10/11 add the tree's own action keys. Bubble Tea reports the space bar as `msg.String() == " "`; verify against `tea.KeySpace`'s `String()` in `charmbracelet/bubbletea`'s key.go if uncertain, but `" "` is the documented value.)

Render the Loops section in `viewFleet`, right after the `builderMsg` block and before the existing workers loop — add:

```go
	if len(m.loops) > 0 {
		b.WriteString(style.SubHeader.Render("Loops") + "\n")
		rows := m.treeRows()
		for i, r := range rows {
			cursor := "  "
			if m.loopsFocused && i == m.treeCursor {
				cursor = style.Marker.Render("▸ ")
			}
			if r.Kind == "loop" {
				loop, _ := m.loopByName(r.LoopName)
				status := "[off]"
				if loop.Enabled {
					status = "[on]"
				}
				running := ""
				if loop.RunID != "" {
					running = fmt.Sprintf("  running (%s)", loop.RunID)
				}
				fmt.Fprintf(&b, "%s%-20s %s%s\n", cursor, loop.Name, status, running)
			} else {
				loop, _ := m.loopByName(r.LoopName)
				fmt.Fprintf(&b, "%s    %d. %s\n", cursor, r.StepIndex+1, loop.Steps[r.StepIndex])
			}
		}
		b.WriteString("\n")
	}
```

Note this rendering is intentionally minimal for this task — Task 10 adds the loop-action keybinding legend, Task 11 wires `c`/`e`/`d`/`shift+up/down` on step rows to the embedded builder. Don't add a footer legend line for those keys yet (it would advertise keys that don't work until Task 10/11 land) — this task's footer stays exactly as it is today.

To avoid showing two `▸` cursors at once (one in each section), also gate the *existing* Workers-table cursor marker on focus: find the pre-existing loop in `viewFleet` that renders Workers rows (`for i, r := range rows { cursor := "  "; if i == m.cursor { ... } ... }`, right below where the Loops section is being inserted) and change its condition from `if i == m.cursor` to `if !m.loopsFocused && i == m.cursor`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tui/... -run TestModel_LoopsSnapshot -v`
Run: `go test ./tui/... -run TestModel_ExpandingLoop -v`
Run: `go test ./tui/... -run TestModel_UpDownMovesWorkersCursorByDefault -v`
Run: `go test ./tui/... -run TestModel_TabSwitchesFocusToLoopsTree -v`
Run: `go test ./tui/... -run TestView_FleetShowsLoopsSection -v`
Expected: PASS

- [ ] **Step 5: Run the full tui suite**

Run: `go test ./tui/...`
Expected: PASS — confirm no existing test asserted on `viewFleet`'s exact output such that the new "Loops" block breaks it (existing tests construct `Model` via `NewModel(Options{})` with no `LoopsSnapshotMsg` sent, so `m.loops` stays empty and the new block renders nothing — this should not break anything, but verify).

- [ ] **Step 6: Commit**

```bash
git add tui/model.go tui/model_test.go tui/view_test.go
git commit -m "feat(tui): render a Loops section with expand/collapse tree navigation"
```

---

### Task 10: Model — loop-level actions (toggle, run-once, graceful/hard stop, rename, delete)

**Files:**
- Modify: `tui/model.go`
- Test: `tui/view_test.go`

**Interfaces:**
- Consumes: `Options.SetLoopEnabledFn/RunLoopOnceFn/StopLoopGracefulFn/RenameLoopFn/DeleteLoopFn` (Task 8), `Options.RespondFn`-style pattern for hard abort (reuses the *existing* `StopLoop` RPC — expose it the same way as `AttachFn`/`RespondFn`: add one more Options field, `AbortLoopFn func(runID string) tea.Cmd`, wired in `tui/program.go` to the pre-existing `StopLoop` RPC. This was omitted from Task 8 because Task 8 only covered the *new* RPCs — add it now since it belongs with this task's key handling.)
- Produces: `t`/`o`/`g`/`x` handling on a loop row; a rename/delete confirmation sub-stage mirroring the existing naming-stage pattern used elsewhere in this codebase family (see `builder`'s `awaitingConcurrency` stage for the pattern of a small modal-like stage layered on the main view).

- [ ] **Step 1: Add `AbortLoopFn` to Options and wire it in `program.go`**

In `tui/model.go`'s `Options`, add near `StopLoopGracefulFn`:

```go
	// AbortLoopFn hard-stops a run immediately (may interrupt an in-flight
	// step), reusing the pre-existing StopLoop RPC.
	AbortLoopFn func(runID string) tea.Cmd
```

In `tui/program.go`, add a builder mirroring `stopLoopGracefulFn` but calling the existing `StopLoop` RPC:

```go
// abortLoopFn returns the Options.AbortLoopFn implementation: an
// immediate hard stop via the pre-existing StopLoop RPC (may interrupt an
// in-flight step), as opposed to StopLoopGracefulFn's finish-then-stop.
func abortLoopFn(ctx context.Context, cl rpc.LooperClient, baseDir string) func(string) tea.Cmd {
	return func(runID string) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.StopLoop(rctx, &rpc.StopLoopRequest{RunId: runID})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()
		}
	}
}
```

Add `AbortLoopFn: abortLoopFn(ctx, cl, baseDir),` to the `NewModel(Options{...})` literal in `Run`.

- [ ] **Step 2: Write the failing tests**

Add to `tui/view_test.go`:

```go
func TestView_ToggleEnabledKeyInvokesSetLoopEnabledFn(t *testing.T) {
	var gotName string
	var gotEnabled bool
	m := NewModel(Options{
		SetLoopEnabledFn: func(name string, enabled bool) tea.Cmd {
			gotName, gotEnabled = name, enabled
			return nil
		},
	})
	next, _ := m.Update(LoopsSnapshotMsg{{Name: "a", Enabled: false}})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // loop-row keys require the Loops tree focused
	m = next.(Model)

	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}); cmd == nil {
		t.Fatalf("'t' on a loop row returned a nil cmd")
	}
	if gotName != "a" || gotEnabled != true {
		t.Errorf("SetLoopEnabledFn called with (%q, %v), want (\"a\", true)", gotName, gotEnabled)
	}
}

func TestView_RunOnceKeyInvokesRunLoopOnceFn(t *testing.T) {
	var got string
	m := NewModel(Options{RunLoopOnceFn: func(name string) tea.Cmd { got = name; return nil }})
	next, _ := m.Update(LoopsSnapshotMsg{{Name: "a"}})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if got != "a" {
		t.Errorf("RunLoopOnceFn called with %q, want \"a\"", got)
	}
}

func TestView_GracefulAndHardStopKeysOnlyActWithAnActiveRun(t *testing.T) {
	var gracefulCalled, abortCalled bool
	m := NewModel(Options{
		StopLoopGracefulFn: func(runID string) tea.Cmd { gracefulCalled = true; return nil },
		AbortLoopFn:        func(runID string) tea.Cmd { abortCalled = true; return nil },
	})
	next, _ := m.Update(LoopsSnapshotMsg{{Name: "a"}}) // no RunID: not running
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if gracefulCalled || abortCalled {
		t.Errorf("graceful/hard stop must be no-ops on a non-running loop")
	}

	next, _ = m.Update(LoopsSnapshotMsg{{Name: "a", RunID: "run-001"}}) // loopsFocused survives this update
	m = next.(Model)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if !gracefulCalled {
		t.Errorf("'g' on a running loop must call StopLoopGracefulFn")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./tui/... -run TestView_ToggleEnabledKey -v`
Run: `go test ./tui/... -run TestView_RunOnceKey -v`
Run: `go test ./tui/... -run TestView_GracefulAndHardStopKeys -v`
Expected: FAIL — `t`/`o`/`g`/`x` currently only handled in `viewFocus`, not on a loop row in `viewFleet`.

- [ ] **Step 4: Implement the key handling**

In `handleKey`, the existing `case "a", "r", "x":` only fires `handleFocusKey` when `m.view == viewFocus`. Add a new function `handleLoopRowKey` and call it for the loop-action keys when in `viewFleet` **with the Loops tree focused** (`m.loopsFocused`, from Task 9 — these keys act on the tree, so require the same `tab`-to-focus step as tree navigation itself; this also means they're all safe no-ops for every pre-existing test, which never sets `loopsFocused`). Add these cases to `handleKey`'s switch (the existing `"a", "r", "x"` case stays as-is for the focus view — these are new, distinct keys, so no collision):

```go
	case "t", "o", "g", "x", "R", "D":
		if m.view == viewFleet && m.loopsFocused {
			return m.handleLoopRowKey(msg.String())
		}
```

Add `handleLoopRowKey` near `handleFocusKey`:

```go
// handleLoopRowKey implements the Loops-section loop-row action keys: t
// toggles enabled, o runs once, g gracefully stops an active run, x hard-
// aborts one, R begins a rename, D begins a delete confirmation. All are
// no-ops when the cursor isn't on a loop row, and g/x are additionally
// no-ops when that loop has no active run.
func (m Model) handleLoopRowKey(k string) (tea.Model, tea.Cmd) {
	rows := m.treeRows()
	if m.treeCursor >= len(rows) || rows[m.treeCursor].Kind != "loop" {
		return m, nil
	}
	loop, ok := m.loopByName(rows[m.treeCursor].LoopName)
	if !ok {
		return m, nil
	}

	switch k {
	case "t":
		if m.opts.SetLoopEnabledFn != nil {
			return m, m.opts.SetLoopEnabledFn(loop.Name, !loop.Enabled)
		}
	case "o":
		if m.opts.RunLoopOnceFn != nil {
			return m, m.opts.RunLoopOnceFn(loop.Name)
		}
	case "g":
		if loop.RunID != "" && m.opts.StopLoopGracefulFn != nil {
			return m, m.opts.StopLoopGracefulFn(loop.RunID)
		}
	case "x":
		if loop.RunID != "" && m.opts.AbortLoopFn != nil {
			return m, m.opts.AbortLoopFn(loop.RunID)
		}
	case "R":
		m.renamingLoop = loop.Name
		m.renameInput = loop.Name
	case "D":
		m.deletingLoop = loop.Name
	}
	return m, nil
}
```

Add the rename/delete confirmation state to `Model` (near `treeCursor`):

```go
	renamingLoop string // "" = not renaming; else the loop name being renamed
	renameInput  string
	deletingLoop string // "" = no delete confirmation pending; else the loop name
```

Route to a new sub-stage in `Update`'s `tea.KeyMsg` case, before the existing `viewBuilder` check:

```go
		if m.renamingLoop != "" {
			return m.handleRenameKey(msg)
		}
		if m.deletingLoop != "" {
			return m.handleDeleteConfirmKey(msg)
		}
```

Add the two handlers near `handleNamingKey`-style code (this codebase has no `handleNamingKey` currently — this is the first modal-input stage on top of `viewFleet`, so write it directly):

```go
// handleRenameKey handles the rename-loop input stage entered via R:
// printable runes append to renameInput, backspace removes the last rune,
// esc cancels, enter confirms and invokes RenameLoopFn.
func (m Model) handleRenameKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.renamingLoop = ""
		m.renameInput = ""
		return m, nil
	case "backspace":
		if r := []rune(m.renameInput); len(r) > 0 {
			m.renameInput = string(r[:len(r)-1])
		}
		return m, nil
	case "enter":
		newName := strings.TrimSpace(m.renameInput)
		oldName := m.renamingLoop
		m.renamingLoop = ""
		m.renameInput = ""
		if newName == "" || m.opts.RenameLoopFn == nil {
			return m, nil
		}
		return m, m.opts.RenameLoopFn(oldName, newName)
	}
	if key.Type == tea.KeyRunes {
		m.renameInput += string(key.Runes)
	}
	return m, nil
}

// handleDeleteConfirmKey handles the delete-loop confirmation stage
// entered via D: y confirms and invokes DeleteLoopFn, any other key
// cancels.
func (m Model) handleDeleteConfirmKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	name := m.deletingLoop
	m.deletingLoop = ""
	if key.String() == "y" && m.opts.DeleteLoopFn != nil {
		return m, m.opts.DeleteLoopFn(name)
	}
	return m, nil
}
```

Render the two sub-stages in `View` (add cases before the `default: return m.viewFleet()`):

```go
	switch {
	case m.renamingLoop != "":
		return m.viewRenameLoop()
	case m.deletingLoop != "":
		return m.viewDeleteConfirm()
	}
```

(Place this check at the top of `View`, before the existing `switch m.view`.)

Add the two render functions near `viewNaming`-style code (adjacent to `viewFleet`):

```go
// viewRenameLoop renders the rename-loop input prompt entered via R.
func (m Model) viewRenameLoop() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.Title.Render(fmt.Sprintf("Rename loop %q", m.renamingLoop)))
	fmt.Fprintf(&b, "%s %s\n", style.Label.Render("New name:"), m.renameInput)
	b.WriteString("\n" + style.KeyHint.Render("[enter] confirm  [esc] cancel") + "\n")
	return b.String()
}

// viewDeleteConfirm renders the delete-loop confirmation prompt entered via D.
func (m Model) viewDeleteConfirm() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.TitleAlert.Render(fmt.Sprintf("Delete loop %q?", m.deletingLoop)))
	b.WriteString("\n" + style.KeyHint.Render("[y] confirm  [any other key] cancel") + "\n")
	return b.String()
}
```

Update the Loops-section footer hint in `viewFleet` (the line added in Task 9 rendering the section) — extend the existing footer line to mention the new keys:

```go
	b.WriteString("\n" + style.KeyHint.Render("[up/down] move  [tab] switch focus  [space] expand/collapse  [enter] focus  [t] toggle  [o] run once  [g] graceful stop  [x] abort  [R] rename  [D] delete  [n] new loop  [q] quit") + "\n")
```

(This replaces the existing shorter footer string — find it via `grep -n "up/down] move  \[enter\] focus" tui/model.go`.)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./tui/... -run TestView_ToggleEnabledKey -v`
Run: `go test ./tui/... -run TestView_RunOnceKey -v`
Run: `go test ./tui/... -run TestView_GracefulAndHardStopKeys -v`
Expected: PASS

- [ ] **Step 6: Run the full tui suite, then whole repo**

Run: `go test ./tui/...`
Run: `go build ./... && go test ./...`
Expected: PASS — pay particular attention to `TestView_FleetFooterMentionsNewLoopKey` (existing test) still passing given the footer line changed; update that test's assertion if it does an exact-string match rather than a `strings.Contains` for `"[n] new loop"`.

- [ ] **Step 7: Commit**

```bash
git add tui/model.go tui/program.go tui/view_test.go
git commit -m "feat(tui): loop-row actions — toggle enabled, run once, graceful/hard stop, rename, delete"
```

---

### Task 11: Model — inline step editing via embedded builder.Model per expanded loop

**Files:**
- Modify: `tui/model.go`
- Test: `tui/view_test.go`

**Interfaces:**
- Consumes: `builder.New`, `builder.Model.Update/View/Steps/StepErrors`, `builder.SessionDoneMsg` (existing, unchanged).
- Produces: `Model.loopBuilders map[string]builder.Model` — one lazily-constructed `builder.Model` per loop name that has ever been expanded, so cursor/authoring state survives collapse/re-expand. `c`/`e`/`d`/`shift+up`/`shift+down` on the expanded loop's rows forward to its `builder.Model`; the loop's step list re-renders from `builder.Model.Steps()` instead of the static `LoopSnapshot.Steps` once a builder exists for it (so an edit is visible immediately, without waiting for the next polled `LoopsSnapshotMsg`).

- [ ] **Step 1: Write the failing tests**

Add to `tui/view_test.go`:

```go
func TestView_ExpandedLoopForwardsCreateStepKeyToEmbeddedBuilder(t *testing.T) {
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	loopPath := filepath.Join(loopsDir, "a.yaml")
	if err := os.WriteFile(loopPath, []byte("name: a\nsteps: []\n"), 0o644); err != nil {
		t.Fatalf("write loop: %v", err)
	}

	var authorReq builder.AuthorRequest
	m := NewModel(Options{
		ProjectDir: dir,
		AuthorFn: func(req builder.AuthorRequest) tea.Cmd {
			authorReq = req
			return nil
		},
	})
	next, _ := m.Update(LoopsSnapshotMsg{{Name: "a", Path: loopPath, Steps: nil}})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // focus the Loops tree
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace}) // expand
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")}) // create-step
	m = next.(Model)

	if authorReq.LoopPath != loopPath {
		t.Errorf("AuthorFn called with LoopPath %q, want %q (create-step key must forward to the expanded loop's embedded builder)", authorReq.LoopPath, loopPath)
	}
}
```

Add `"os"`, `"path/filepath"`, and `"github.com/jbofill10/looper/builder"` to `tui/view_test.go`'s imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tui/... -run TestView_ExpandedLoopForwardsCreateStepKey -v`
Expected: FAIL — `c` currently does nothing in `viewFleet` (it isn't handled at all there yet).

- [ ] **Step 3: Add the embedded-builder map and forwarding**

Add to `Model` (near `expandedLoop`):

```go
	loopBuilders map[string]builder.Model // lazily populated, one per loop ever expanded
```

Initialize it in `NewModel`:

```go
func NewModel(opts Options) Model {
	return Model{
		opts:         opts,
		workers:      map[workerKey]workerRow{},
		loopBuilders: map[string]builder.Model{},
	}
}
```

Add a helper that lazily constructs a loop's embedded builder:

```go
// loopBuilder returns the builder.Model backing loopName's inline step
// list, constructing it (via builder.New, against that loop's own file —
// see LoopSnapshot.Path) the first time it's needed, and caching it so
// cursor/authoring state survives collapsing and re-expanding the loop.
func (m *Model) loopBuilder(loopName string) (builder.Model, bool) {
	if b, ok := m.loopBuilders[loopName]; ok {
		return b, true
	}
	loop, ok := m.loopByName(loopName)
	if !ok {
		return builder.Model{}, false
	}
	b, err := builder.New(m.opts.ProjectDir, loop.Path, builder.Options{AuthorFn: m.opts.AuthorFn})
	if err != nil {
		return builder.Model{}, false
	}
	m.loopBuilders[loopName] = b
	return b, true
}
```

In `treeRows` and the Loops-section render in `viewFleet` (Task 9), the step count/names for an *expanded* loop should come from its embedded builder once one exists, not the static snapshot — otherwise an add/delete/reorder wouldn't show until the next 2-second poll. Update `treeRows`:

```go
func (m Model) treeRows() []treeRow {
	var rows []treeRow
	for _, l := range m.loops {
		rows = append(rows, treeRow{Kind: "loop", LoopName: l.Name})
		if l.Name == m.expandedLoop {
			n := len(l.Steps)
			if b, ok := m.loopBuilders[l.Name]; ok {
				n = len(b.Steps())
			}
			for i := 0; i < n; i++ {
				rows = append(rows, treeRow{Kind: "step", LoopName: l.Name, StepIndex: i})
			}
		}
	}
	return rows
}
```

Update the step-row render line in `viewFleet` (Task 9's `else` branch) to prefer the builder's step name/type when available:

```go
			} else {
				stepName := ""
				if l, ok := m.loopByName(r.LoopName); ok && r.StepIndex < len(l.Steps) {
					stepName = l.Steps[r.StepIndex]
				}
				if b, ok := m.loopBuilders[r.LoopName]; ok {
					if steps := b.Steps(); r.StepIndex < len(steps) {
						stepName = fmt.Sprintf("%s (%s)", steps[r.StepIndex].Name, steps[r.StepIndex].Type)
					}
				}
				fmt.Fprintf(&b, "%s    %d. %s\n", cursor, r.StepIndex+1, stepName)
			}
```

(Note the shadowing: the render loop's `strings.Builder` receiver is already named `b` in `viewFleet` — rename the loop's local `builder.Model` variable to `bm` to avoid colliding: `if bm, ok := m.loopBuilders[r.LoopName]; ok { if steps := bm.Steps(); ... } `.)

Forward `c`/`e`/`d`/`shift+up`/`shift+down` to the expanded loop's builder, again gated on `m.loopsFocused` for the same reason as Task 10's loop-row keys. Add a case to `handleKey`'s switch, before the `"t", "o", "g", "x", "R", "D"` case added in Task 10:

```go
	case "c", "e", "d", "shift+up", "shift+down":
		if m.view == viewFleet && m.loopsFocused && m.expandedLoop != "" {
			return m.handleExpandedLoopStepKey(msg)
		}
```

Add `handleExpandedLoopStepKey` near `handleLoopRowKey`:

```go
// handleExpandedLoopStepKey forwards a step-list key (c/e/d/shift+up/
// shift+down) to the expanded loop's embedded builder.Model, positioning
// that builder's own cursor at the tree's currently-selected step row
// first (if the cursor is on a step row of this loop) so the forwarded
// key acts on the right step.
func (m Model) handleExpandedLoopStepKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	b, ok := m.loopBuilder(m.expandedLoop)
	if !ok {
		return m, nil
	}

	rows := m.treeRows()
	if m.treeCursor < len(rows) {
		row := rows[m.treeCursor]
		if row.Kind == "step" && row.LoopName == m.expandedLoop {
			b = b.WithCursor(row.StepIndex)
		}
	}

	next, cmd := b.Update(key)
	m.loopBuilders[m.expandedLoop] = next.(builder.Model)
	return m, cmd
}
```

`builder.Model` has no `WithCursor` method yet — add one (small, additive) in `builder/builder.go`:

```go
// WithCursor returns m with its cursor set to i, clamped to a valid step
// index. Used by embedders (e.g. the fleet TUI's inline step list) that
// track their own selection and need the builder's next c/e/d/reorder key
// to act on that same step.
func (m Model) WithCursor(i int) Model {
	if i < 0 {
		i = 0
	}
	if max := len(m.loop.Steps) - 1; i > max {
		i = max
	}
	if i < 0 {
		i = 0
	}
	m.cursor = i
	return m
}
```

Route `builder.SessionDoneMsg` to the *expanded* loop's builder instead of the old single `m.builder` field when a loop is expanded. Update the `Update` method's existing case:

```go
	case builder.SessionDoneMsg:
		if m.view == viewBuilder {
			next, cmd := m.builder.Update(msg)
			m.builder = next.(builder.Model)
			return m, cmd
		}
		if m.expandedLoop != "" {
			if b, ok := m.loopBuilders[m.expandedLoop]; ok {
				next, cmd := b.Update(msg)
				m.loopBuilders[m.expandedLoop] = next.(builder.Model)
				return m, cmd
			}
		}
		return m, nil
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tui/... -run TestView_ExpandedLoopForwardsCreateStepKey -v`
Expected: PASS

- [ ] **Step 5: Run the full tui suite, then whole repo**

Run: `go test ./tui/...`
Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add tui/model.go builder/builder.go tui/view_test.go
git commit -m "feat(tui): inline step add/edit/delete/reorder via the expanded loop's embedded builder"
```

---

### Task 12: Manual verification pass

**Files:** none (no code changes) — this task is a checklist, not a diff.

- [ ] **Step 1: Build and run the TUI against a real daemon**

Run: `go build -o looper . && ./looper daemon --socket /tmp/looper-verify.sock &` then, in the same directory as a `.looper/loops/` with at least one loop file, `./looper tui --socket /tmp/looper-verify.sock`.

- [ ] **Step 2: Walk the golden path**

Confirm, interactively:
- The Loops section lists every `.looper/loops/*.yaml` file, `[off]` by default.
- `t` on a loop toggles it `[on]` and a run starts (shows up with a run id; the existing Workers table below picks up its worker rows once it starts producing them).
- `space` expands a loop to show its steps; `space` again collapses it.
- With a loop expanded: `c` opens a create-step session (via the configured `claude` harness), `e` edits the selected step, `d` deletes it, `shift+up`/`shift+down` reorder it — and the step list updates immediately after each.
- `o` on a loop starts a one-off run (confirm via `./looper ls --socket /tmp/looper-verify.sock` in another terminal that it ran exactly one iteration).
- `g` on a running loop lets its current iteration finish before stopping (watch a slower step's session survive to completion); `x` interrupts immediately.
- `R` renames a loop (file renamed on disk, tree reflects the new name); `D` prompts and, on `y`, deletes the file.
- Stop the daemon (`kill %1` or `./looper shutdown --socket /tmp/looper-verify.sock`), restart it the same way, and confirm any loop left `[on]` auto-resumes without re-toggling it.

- [ ] **Step 3: Report results**

If every item in Step 2 behaves as described, this plan is complete. If anything diverges, note exactly which item and what happened instead — do not mark this task done until the discrepancy is either fixed (new task/commit) or explicitly accepted as a follow-up by the user.
