# Relocate Default Workdir Under `.looper/` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Steps (`script`/`headless`/`interactive`) no longer execute in the shared project repo root by default; they get a per-loop, per-worker directory under `.looper/work/`, so relative-path step output stops polluting the project's working tree.

**Architecture:** `Worker.workDir()` (new, in `runner/worker.go`) computes `<BaseDir>/work/<loop-name>/<worker-id>/` (or `<BaseDir>/work/<loop-name>/` when the worker has no ID) and creates it on demand. The two call sites in `runIteration` that currently do `rc.Set("WORKDIR", w.Workdir)` call this instead. The `Worker.Workdir` field, and the now-dead `RunOptions.Workdir` plumbing that only ever fed it, are removed. The daemon/RPC-level `workdir` parameter (used for other purposes — registry persistence, `workdirFromBaseDir`) is untouched except for deleting the one line that fed it into `runner.Worker{}`.

**Tech Stack:** Go 1.26, standard library only (`os`, `path/filepath`).

## Global Constraints

- `go build ./...` and `go test ./...` must pass before each commit (per this repo's CLAUDE.md workflow rule).
- Every change goes through a PR into `main` — this plan assumes it's executed on a feature branch (already created: `feat/workdir-under-looper`).
- No changes to the RPC/protobuf contract, the daemon's registry persistence format, or `workspace: worktree`/git-worktree isolation — all explicitly out of scope per `docs/superpowers/specs/2026-07-22-workdir-under-looper-design.md`.

---

### Task 1: Give `Worker` a computed default workdir under `.looper/work/`

**Files:**
- Modify: `runner/worker.go`
- Test: `runner/worker_test.go`

**Interfaces:**
- Produces: `func (w *Worker) workDir() (string, error)` — an unexported method on `Worker`. Returns `filepath.Join(w.BaseDir, "work", w.Loop.Name[, w.ID])`, creating it via `os.MkdirAll` if missing. Called only from within `runner/worker.go`; no other package needs it.
- Consumes: existing `Worker` fields `BaseDir`, `Loop.Name`, `ID` (all already set by every caller — `cli/run.go`, `daemon/manager.go`, tests).

- [ ] **Step 1: Write the failing tests**

Add these three tests to the end of `runner/worker_test.go`:

```go
// TestWorker_WorkdirDefaultsUnderBaseDirPerWorker asserts that a worker with
// an ID gets WORKDIR set to BaseDir/work/<loop>/<worker-id>, and that the
// directory actually exists on disk after the run.
func TestWorker_WorkdirDefaultsUnderBaseDirPerWorker(t *testing.T) {
	base := filepath.Join(t.TempDir(), ".looper")
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	if err := loop.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	w := &Worker{Loop: loop, BaseDir: base, Prompter: &FakePrompter{}, ID: "w1", NewID: idSeq()}
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := filepath.Join(base, "work", "l", "w1")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected workdir %s to exist: %v", want, err)
	}

	rc, err := runctx.Load(filepath.Join(base, "runs", "l", "w1", "iter-1"))
	if err != nil {
		t.Fatalf("load run context: %v", err)
	}
	if got, _ := rc.Get("WORKDIR"); got != want {
		t.Errorf("WORKDIR = %q, want %q", got, want)
	}
}

// TestWorker_WorkdirDefaultsUnderBaseDirNoID asserts the single-worker
// (`looper run`) case, where Worker.ID is unset: WORKDIR should be
// BaseDir/work/<loop>, with no ID segment.
func TestWorker_WorkdirDefaultsUnderBaseDirNoID(t *testing.T) {
	base := filepath.Join(t.TempDir(), ".looper")
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	if err := loop.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	w := &Worker{Loop: loop, BaseDir: base, Prompter: &FakePrompter{}, NewID: idSeq()}
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := filepath.Join(base, "work", "l")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected workdir %s to exist: %v", want, err)
	}
}

// TestWorker_WorkdirIsolatedPerLoopAndWorker asserts two workers for
// different loops (sharing the same BaseDir, same worker ID "w1") get
// distinct workdirs, each created on disk.
func TestWorker_WorkdirIsolatedPerLoopAndWorker(t *testing.T) {
	base := filepath.Join(t.TempDir(), ".looper")
	mkLoop := func(name string) *config.Loop {
		l := &config.Loop{
			Name:          name,
			MaxIterations: 1,
			Steps:         []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
		}
		if err := l.Validate(); err != nil {
			t.Fatalf("validate %s: %v", name, err)
		}
		return l
	}
	w1 := &Worker{Loop: mkLoop("a"), BaseDir: base, Prompter: &FakePrompter{}, ID: "w1", NewID: idSeq()}
	w2 := &Worker{Loop: mkLoop("b"), BaseDir: base, Prompter: &FakePrompter{}, ID: "w1", NewID: idSeq()}
	if err := w1.Run(); err != nil {
		t.Fatalf("w1 Run: %v", err)
	}
	if err := w2.Run(); err != nil {
		t.Fatalf("w2 Run: %v", err)
	}

	dirA := filepath.Join(base, "work", "a", "w1")
	dirB := filepath.Join(base, "work", "b", "w1")
	if _, err := os.Stat(dirA); err != nil {
		t.Errorf("dirA %s missing: %v", dirA, err)
	}
	if _, err := os.Stat(dirB); err != nil {
		t.Errorf("dirB %s missing: %v", dirB, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./runner/... -run TestWorker_Workdir -v`

Expected: all three FAIL. `TestWorker_WorkdirDefaultsUnderBaseDirPerWorker` and `_NoID` fail because `WORKDIR` currently resolves to `w.Workdir` (unset, so `""`), so `.../work/l/w1` (or `.../work/l`) is never created — `os.Stat` errors with "no such file or directory". `TestWorker_WorkdirIsolatedPerLoopAndWorker` fails the same way.

- [ ] **Step 3: Implement `workDir()` and wire it in, removing the `Workdir` field**

In `runner/worker.go`, add `"os"` to the import block (currently `"context"`, `"fmt"`, `"path/filepath"`, `"strings"`, `"sync"`, `"time"`, plus the two internal packages):

```go
import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runctx"
)
```

Remove the `Workdir` field from the `Worker` struct (currently line 44):

```go
	Loop        *config.Loop
	BaseDir     string // the .looper dir
	Workdir     string // execution dir (workspace: shared)
	Prompter    Prompter
```

becomes:

```go
	Loop        *config.Loop
	BaseDir     string // the .looper dir
	Prompter    Prompter
```

Add the `workDir` method. Place it right after `idGen()` (currently ends around line 164), before `func (w *Worker) Run() error`:

```go
// workDir returns this worker's execution directory for script/headless/
// interactive steps: BaseDir/work/<loop name>/<worker ID>, or
// BaseDir/work/<loop name> when the worker has no ID (single-worker
// `looper run`) — mirroring the ID-namespacing runIteration already
// applies to run-history directories. Creates the directory if missing.
func (w *Worker) workDir() (string, error) {
	dir := filepath.Join(w.BaseDir, "work", w.Loop.Name)
	if w.ID != "" {
		dir = filepath.Join(dir, w.ID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir workdir %s: %w", dir, err)
	}
	return dir, nil
}
```

In `runIteration`, replace the resuming branch (currently):

```go
		if v, ok := rc.Get("WORKDIR"); !ok || v == "" {
			rc.Set("WORKDIR", w.Workdir)
		}
```

with:

```go
		if v, ok := rc.Get("WORKDIR"); !ok || v == "" {
			wd, err := w.workDir()
			if err != nil {
				return false, err
			}
			rc.Set("WORKDIR", wd)
		}
```

And replace the fresh-iteration line (currently):

```go
		rc.Set("WORKDIR", w.Workdir)
```

with:

```go
		wd, err := w.workDir()
		if err != nil {
			return false, err
		}
		rc.Set("WORKDIR", wd)
```

Now fix the three existing test call sites in `runner/worker_test.go` that construct a `Worker` with a `Workdir:` field — they no longer compile once the field is removed.

In `newWorker` (currently lines 22-33):

```go
func newWorker(t *testing.T, loop *config.Loop, p Prompter) *Worker {
	t.Helper()
	base := filepath.Join(t.TempDir(), ".looper")
	work := t.TempDir()
	return &Worker{
		Loop:     loop,
		BaseDir:  base,
		Workdir:  work,
		Prompter: p,
		NewID:    idSeq(),
	}
}
```

becomes:

```go
func newWorker(t *testing.T, loop *config.Loop, p Prompter) *Worker {
	t.Helper()
	base := filepath.Join(t.TempDir(), ".looper")
	return &Worker{
		Loop:     loop,
		BaseDir:  base,
		Prompter: p,
		NewID:    idSeq(),
	}
}
```

In `TestWorker_AcquireLockSerializesAcrossWorkers`, remove the `Workdir: dir,` line from the `Worker{}` literal (currently around line 388):

```go
			w := &Worker{
				Loop:        loop,
				BaseDir:     base,
				Workdir:     dir,
				Prompter:    &FakePrompter{},
				ID:          id,
				AcquireLock: &lock,
			}
```

becomes:

```go
			w := &Worker{
				Loop:        loop,
				BaseDir:     base,
				Prompter:    &FakePrompter{},
				ID:          id,
				AcquireLock: &lock,
			}
```

In `TestWorker_GracefulStopEndsAfterCurrentIteration`, remove the `Workdir: dir,` line (currently around line 655):

```go
	w := &Worker{
		Loop:         loop,
		BaseDir:      filepath.Join(dir, ".looper"),
		Workdir:      dir,
		GracefulStop: graceful,
```

becomes:

```go
	w := &Worker{
		Loop:         loop,
		BaseDir:      filepath.Join(dir, ".looper"),
		GracefulStop: graceful,
```

- [ ] **Step 4: Run the full runner package test suite**

Run: `go test ./runner/... -v`

Expected: PASS for all tests, including the three new ones and the three edited ones.

- [ ] **Step 5: Commit**

```bash
git add runner/worker.go runner/worker_test.go
git commit -m "$(cat <<'EOF'
feat(runner): default step workdir to .looper/work/<loop>/<worker>
EOF
)"
```

---

### Task 2: Remove the now-dead `RunOptions.Workdir` plumbing in the CLI package

**Files:**
- Modify: `cli/run.go`
- Modify: `cli/resume.go`
- Test: `cli/run_test.go`, `cli/resume_test.go`

**Interfaces:**
- Consumes: Task 1's `Worker.workDir()` (indirectly — `runner.Worker{}` no longer takes a `Workdir` field, so `RunOptions.Workdir` has nothing left to feed).
- Produces: nothing new. This task only deletes dead surface.

`RunOptions.Workdir` was the CLI-side origin of `Worker.Workdir` (`opts.Workdir` → `Workdir: opts.Workdir` in `RunLoop`). With `Worker.Workdir` gone (Task 1), this field is write-only dead code: `os.Getwd()` result assigned to it, never read anywhere else (confirmed via `grep -rn "opts.Workdir\|RunOptions" cli/`). Both `newRunCmd` and `newResumeCmd` already compute `wd` independently for `BaseDir`, so removing this doesn't touch how `BaseDir` is derived.

- [ ] **Step 1: Remove the field and its two setters**

In `cli/run.go`, remove the `Workdir` field from `RunOptions` (currently line 32):

```go
type RunOptions struct {
	LoopName string    // loads BaseDir/loops/<LoopName>.yaml when File is empty
	File     string    // explicit loop file path (overrides LoopName)
	BaseDir  string    // the .looper directory
	Workdir  string    // execution dir for workspace: shared
	In       io.Reader // prompter input (defaults to os.Stdin)
	Out      io.Writer // prompter/output (defaults to os.Stdout)
```

becomes:

```go
type RunOptions struct {
	LoopName string    // loads BaseDir/loops/<LoopName>.yaml when File is empty
	File     string    // explicit loop file path (overrides LoopName)
	BaseDir  string    // the .looper directory
	In       io.Reader // prompter input (defaults to os.Stdin)
	Out      io.Writer // prompter/output (defaults to os.Stdout)
```

Remove `Workdir: opts.Workdir,` from the `runner.Worker{}` construction in `RunLoop` (currently line 77):

```go
	w := &runner.Worker{
		Loop:      loop,
		BaseDir:   opts.BaseDir,
		Workdir:   opts.Workdir,
		Prompter:  &runner.StdinPrompter{In: in, Out: out},
```

becomes:

```go
	w := &runner.Worker{
		Loop:      loop,
		BaseDir:   opts.BaseDir,
		Prompter:  &runner.StdinPrompter{In: in, Out: out},
```

Remove `Workdir: wd,` from `newRunCmd` (currently line 106):

```go
			opts := RunOptions{
				File:    file,
				BaseDir: filepath.Join(wd, ".looper"),
				Workdir: wd,
				In:      cmd.InOrStdin(),
				Out:     cmd.OutOrStdout(),
			}
```

becomes:

```go
			opts := RunOptions{
				File:    file,
				BaseDir: filepath.Join(wd, ".looper"),
				In:      cmd.InOrStdin(),
				Out:     cmd.OutOrStdout(),
			}
```

In `cli/resume.go`, remove `Workdir: wd,` from `newResumeCmd` (currently line 109):

```go
			opts := RunOptions{
				File:    file,
				BaseDir: baseDir,
				Workdir: wd,
				In:      cmd.InOrStdin(),
				Out:     cmd.OutOrStdout(),
			}
```

becomes:

```go
			opts := RunOptions{
				File:    file,
				BaseDir: baseDir,
				In:      cmd.InOrStdin(),
				Out:     cmd.OutOrStdout(),
			}
```

- [ ] **Step 2: Fix the now-uncompilable test literals**

In `cli/run_test.go`, remove `Workdir:  repo,` from both `RunOptions{}` literals (currently lines 26 and 62):

```go
	err := RunLoop(RunOptions{
		LoopName: "t",
		BaseDir:  filepath.Join(repo, ".looper"),
		Workdir:  repo,
		In:       strings.NewReader(""),
		Out:      &strings.Builder{},
	})
```

becomes (apply the same removal to both occurrences — the second uses `LoopName: "t2"`):

```go
	err := RunLoop(RunOptions{
		LoopName: "t",
		BaseDir:  filepath.Join(repo, ".looper"),
		In:       strings.NewReader(""),
		Out:      &strings.Builder{},
	})
```

In `cli/resume_test.go`, remove `Workdir:   repo,` from the `RunOptions{}` literal (currently line 148):

```go
	err = RunLoop(RunOptions{
		LoopName:  "r",
		BaseDir:   baseDir,
		Workdir:   repo,
		In:        strings.NewReader(""),
		Out:       &strings.Builder{},
		ResumeDir: resumeDir,
	})
```

becomes:

```go
	err = RunLoop(RunOptions{
		LoopName:  "r",
		BaseDir:   baseDir,
		In:        strings.NewReader(""),
		Out:       &strings.Builder{},
		ResumeDir: resumeDir,
	})
```

- [ ] **Step 3: Run the cli package test suite**

Run: `go test ./cli/... -v`

Expected: PASS (compiles clean, all existing tests pass unchanged — none of them asserted on `Workdir`'s value, only on script side effects written via absolute paths).

- [ ] **Step 4: Commit**

```bash
git add cli/run.go cli/resume.go cli/run_test.go cli/resume_test.go
git commit -m "$(cat <<'EOF'
refactor(cli): drop dead RunOptions.Workdir now that Worker computes it
EOF
)"
```

---

### Task 3: Drop the dead `Worker.Workdir` wiring in the daemon

**Files:**
- Modify: `daemon/manager.go`

**Interfaces:**
- Consumes: Task 1 (the `runner.Worker` struct no longer has a `Workdir` field).
- Produces: nothing new.

The daemon's `workdir` parameter (`StartLoop`'s 4th argument, `registryEntry.workdir`, the RPC `workdir` fields) stays exactly as-is — it's part of the RPC contract and registry persistence format and serves `workdirFromBaseDir`'s fallback role, which is out of scope for this change (see spec, "Out of scope"). Only the line that fed it into `runner.Worker{}` is removed, since that field no longer exists.

- [ ] **Step 1: Remove the dead field assignment**

In `daemon/manager.go`, inside `StartLoop`'s per-worker loop, remove `Workdir: workdir,` (currently line 318):

```go
		w := &runner.Worker{
			Loop:         loop,
			BaseDir:      baseDir,
			Workdir:      workdir,
			Prompter:     prompter,
			Global:       m.global,
```

becomes:

```go
		w := &runner.Worker{
			Loop:         loop,
			BaseDir:      baseDir,
			Prompter:     prompter,
			Global:       m.global,
```

- [ ] **Step 2: Run the daemon package test suite**

Run: `go test ./daemon/... -v`

Expected: PASS. Note: this repo's sandboxed environment cannot spawn a live daemon process/socket (confirmed separately, unrelated pre-existing environment limitation) — `TestService_*` tests that need a running daemon may fail with socket-dial timeouts regardless of this change. Confirm this change introduces no *new* failures by comparing against a run on `main` first if any failures appear:

```bash
git stash && go test ./daemon/... 2>&1 | tail -30 && git stash pop
```

- [ ] **Step 3: Commit**

```bash
git add daemon/manager.go
git commit -m "$(cat <<'EOF'
refactor(daemon): drop dead Workdir wiring into runner.Worker
EOF
)"
```

---

### Task 4: Ignore the new work directory in git, and build-check the whole module

**Files:**
- Modify: `.gitignore`

- [ ] **Step 1: Add the ignore rule**

Current `.gitignore`:

```
/looper
.looper/runs/
.looper/loops
```

New `.gitignore`:

```
/looper
.looper/runs/
.looper/loops
.looper/work/
```

- [ ] **Step 2: Full build and test pass**

Run: `go build ./... && go test ./...`

Expected: build succeeds; test results match Task 3's baseline (same pre-existing daemon/cli socket-dial failures, if any, and nothing new).

- [ ] **Step 3: Commit**

```bash
git add .gitignore
git commit -m "$(cat <<'EOF'
chore: gitignore the new per-worker .looper/work/ directory
EOF
)"
```

---

### Task 5: Clean up the stray artifact files already sitting in this repo's root

**Files:**
- Delete: `tasks.json`, `tasks-digest.md`, `task-CLS-1865.json`, `repos-CLS-1865.json`, `repos-digest-CLS-1865.md` (all untracked, all in the repo root)

These are leftover output from running the `get-cfa-tasks`/`cfa-dev-loop` loops against this repo before this fix landed — exactly the pollution this change prevents going forward. They were never tracked by git, so no commit is needed to remove them from history; this is a working-tree cleanup only.

- [ ] **Step 1: Delete the stray files**

```bash
rm -f tasks.json tasks-digest.md task-CLS-1865.json repos-CLS-1865.json repos-digest-CLS-1865.md
```

- [ ] **Step 2: Verify the working tree is clean of stray output**

Run: `git status --short`

Expected: no output for those five filenames. (`.looper/` may still show as untracked — that's the user's own loop configs/run history, out of scope to remove.)
