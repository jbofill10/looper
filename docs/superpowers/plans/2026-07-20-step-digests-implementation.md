# Step Digests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a step declare one of its outputs as its "digest" (a short markdown summary), capture that digest per iteration, and let the fleet TUI browse a loop's run history and view each step's captured digest.

**Architecture:** A new `Step.Digest` config field names an existing/implicit output var; the runner copies that var's file content into `steps/<step>.digest.md` in the run dir right after normal output capture. A new `history` package scans `.looper/runs/<loop>/...` on disk to enumerate iterations and which steps captured a digest. The fleet TUI gets two new views (`viewRuns`, `viewDigest`) reached via a new `h` keybinding on a Loops-catalog row, rendering digest markdown with `glamour`. All I/O (disk scanning, digest reading) happens in `tui/program.go`'s wiring, keeping `tui.Model` pure per its existing convention.

**Tech Stack:** Go 1.26, Bubble Tea/Lipgloss (existing), `github.com/charmbracelet/glamour` (new dependency for markdown rendering).

## Global Constraints

- Module: `github.com/jbofill10/looper`, Go 1.26.4 (see `go.mod`) — do not lower the Go version.
- `tui.Model` must stay side-effect free: all disk/network I/O is performed by closures wired through `Options` function fields (returning `tea.Cmd`), never inside `Model.Update`/`View` directly — this is the established pattern (`RespondFn`, `AttachFn`, `LoadRunHistoryFn`, etc.).
- Follow Conventional Commits for every commit message.
- Per this repo's workflow rule (`CLAUDE.md`), this work lands on its own branch and PR into `main` — do not commit to `main` directly.
- New dependency `github.com/charmbracelet/glamour` must be added via `go get` (updates `go.mod`/`go.sum`), not hand-edited.

---

## Task 1: Config schema — `Step.Digest` field

**Files:**
- Modify: `config/loop.go:32-41` (Step struct), `config/loop.go:126-148` (`Step.Validate`)
- Test: `config/loop_test.go`

**Interfaces:**
- Produces: `config.Step.Digest string` (yaml tag `digest,omitempty`) — names an output var (declared in `Outputs` or implicit) whose captured value is a path to a digest markdown file. Consumed by Task 2's runner changes.

- [ ] **Step 1: Write the failing tests**

Add to `config/loop_test.go`, inside `TestStepValidate_Valid`'s `cases` slice (after the existing four entries):

```go
		{Name: "a", Type: StepScript, Run: "true", Outputs: []string{"X"}, Digest: "Y"},
		{Name: "a", Type: StepHeadless, Prompt: "go", Digest: "DIGEST_FILE"},
```

Add to `TestStepValidate_InvalidCases`'s `cases` map (after the existing six entries):

```go
		"digest duplicates outputs": {Name: "a", Type: StepScript, Run: "true", Outputs: []string{"D"}, Digest: "D"},
		"digest on manual step":     {Name: "a", Type: StepManual, Digest: "D"},
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./config/... -run TestStepValidate -v`
Expected: FAIL — `TestStepValidate_Valid` fails to compile or fails because `Digest` field doesn't exist yet on `Step`.

- [ ] **Step 3: Add the `Digest` field**

In `config/loop.go`, change the `Step` struct (currently lines 32-41):

```go
// Step is one unit of work in a loop.
type Step struct {
	Name          string   `yaml:"name"`
	Type          StepType `yaml:"type"`
	Run           string   `yaml:"run,omitempty"`    // script
	Prompt        string   `yaml:"prompt,omitempty"` // interactive/headless
	Harness       string   `yaml:"harness,omitempty"`
	Outputs       []string `yaml:"outputs,omitempty"`
	Digest        string   `yaml:"digest,omitempty"` // output var holding a path to this step's digest markdown file
	SignalsNoWork bool     `yaml:"signals_no_work,omitempty"`
	OnFail        OnFail   `yaml:"on_fail,omitempty"`
}
```

- [ ] **Step 4: Add validation**

In `config/loop.go`'s `Step.Validate` (currently lines 126-148), insert this block after the interactive/headless prompt check and before the `OnFail` switch:

```go
	if s.Digest != "" {
		if s.Type == StepManual {
			return fmt.Errorf("manual step cannot have digest")
		}
		for _, o := range s.Outputs {
			if o == s.Digest {
				return fmt.Errorf("digest %q must not duplicate an outputs entry", s.Digest)
			}
		}
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./config/... -v`
Expected: PASS, all `config` package tests green.

- [ ] **Step 6: Commit**

```bash
git add config/loop.go config/loop_test.go
git commit -m "feat: add digest field to step config"
```

---

## Task 2: Runner — capture step digests

**Files:**
- Create: `runner/digest.go`, `runner/digest_test.go`
- Modify: `runner/script.go:56-61`, `runner/headless.go:72-77`, `runner/interactive.go:106-110` (guard + call site), `runner/script.go:98-101` (`captureOutputs`'s `declared` map)

**Interfaces:**
- Consumes: `config.Step.Digest` (Task 1), `runctx.RunContext.Get`/`StepsDir` (existing).
- Produces: `steps/<step.Name>.digest.md` in the run dir when `step.Digest` resolves to an existing file. Consumed by Task 3's history scanner (`steps/<name>.digest.md` presence check).

- [ ] **Step 1: Write the failing tests**

Create `runner/digest_test.go`:

```go
package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jbofill10/looper/config"
)

func TestScript_CapturesDigest(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{
		Name: "plan",
		Type: config.StepScript,
		Run: `
			echo "hi" > "$(pwd)/plan-digest.md"
			echo "PLAN_DIGEST_FILE=$(pwd)/plan-digest.md" >> "$LOOPER_OUTPUT"
		`,
		Digest: "PLAN_DIGEST_FILE",
	}
	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(rc.StepsDir(), "plan.digest.md"))
	if err != nil {
		t.Fatalf("read captured digest: %v", err)
	}
	if string(data) != "hi\n" {
		t.Errorf("digest content = %q, want %q", data, "hi\n")
	}
	if _, ok := rc.Get("PLAN_DIGEST_FILE"); !ok {
		t.Errorf("PLAN_DIGEST_FILE should be captured as an implicit output")
	}
}

func TestScript_NoDigestFieldCapturesNothing(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{Name: "plain", Type: config.StepScript, Run: "true"}
	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rc.StepsDir(), "plain.digest.md")); !os.IsNotExist(err) {
		t.Errorf("expected no digest file, got err=%v", err)
	}
}

func TestScript_DigestVarUnsetIsNotError(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{Name: "x", Type: config.StepScript, Run: "true", Digest: "MISSING_VAR"}
	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rc.StepsDir(), "x.digest.md")); !os.IsNotExist(err) {
		t.Errorf("expected no digest file, got err=%v", err)
	}
}
```

(Coverage note: `captureOutputs` and `captureDigest` are shared by all three executors that produce output — script, headless, interactive — so exercising them once through `ScriptExecutor` is sufficient, matching this repo's existing convention of testing shared capture logic only via `runner/script_test.go`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./runner/... -run TestScript_CapturesDigest -v`
Expected: FAIL — compile error, `config.Step` has no field `Digest` until Task 1 lands (Task 1 must be committed first), and/or no `.digest.md` file is produced yet.

- [ ] **Step 3: Add `captureDigest`**

Create `runner/digest.go`:

```go
package runner

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runctx"
)

// captureDigest copies the content of the file at step's digest output var
// (if step.Digest is set and that var resolved to an existing file) into
// steps/<step.Name>.digest.md in rc's run dir. A missing var or missing
// file is not an error — the step simply has no digest for this iteration.
func captureDigest(rc *runctx.RunContext, step config.Step) error {
	if step.Digest == "" {
		return nil
	}
	path, ok := rc.Get(step.Digest)
	if !ok || path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read digest file: %w", err)
	}
	dest := filepath.Join(rc.StepsDir(), step.Name+".digest.md")
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Errorf("write digest: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Make `step.Digest` an implicit output var**

In `runner/script.go`, change `captureOutputs`'s declared-vars setup (currently lines 98-101):

```go
	declared := map[string]bool{}
	for _, k := range step.Outputs {
		declared[k] = true
	}
	if step.Digest != "" {
		declared[step.Digest] = true
	}
```

- [ ] **Step 5: Wire `captureDigest` into the three executors**

In `runner/script.go`, change (currently lines 56-61):

```go
	// Capture declared outputs regardless of exit code.
	if len(step.Outputs) > 0 {
		if err := captureOutputs(rc, step, outPath); err != nil {
			return 0, err
		}
	}
```

to:

```go
	// Capture declared outputs regardless of exit code.
	if len(step.Outputs) > 0 || step.Digest != "" {
		if err := captureOutputs(rc, step, outPath); err != nil {
			return 0, err
		}
	}
	if err := captureDigest(rc, step); err != nil {
		return 0, err
	}
```

In `runner/headless.go`, apply the identical change to the matching block (currently lines 72-77):

```go
	// Capture declared outputs regardless of exit code.
	if len(step.Outputs) > 0 || step.Digest != "" {
		if err := captureOutputs(rc, step, outPath); err != nil {
			return 0, err
		}
	}
	if err := captureDigest(rc, step); err != nil {
		return 0, err
	}
```

In `runner/interactive.go`, apply the identical change to the matching block (currently lines 106-110):

```go
	if len(step.Outputs) > 0 || step.Digest != "" {
		if err := captureOutputs(rc, step, outPath); err != nil {
			return 0, err
		}
	}
	if err := captureDigest(rc, step); err != nil {
		return 0, err
	}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./config/... ./runner/... -v`
Expected: PASS, all `config` and `runner` package tests green.

- [ ] **Step 7: Commit**

```bash
git add runner/digest.go runner/digest_test.go runner/script.go runner/headless.go runner/interactive.go
git commit -m "feat: capture step digests into steps/<name>.digest.md"
```

---

## Task 3: `history` package — scan run directories

**Files:**
- Create: `history/history.go`, `history/history_test.go`

**Interfaces:**
- Consumes: on-disk run dir layout from `runctx` (Task 2's `steps/<name>.digest.md`, existing `context.json`, `progress.json`, `events.jsonl`).
- Produces:
  - `history.StepDigest{Name string, HasDigest bool}`
  - `history.Entry{Dir, IterationID, WorkerID, Status string, Steps []StepDigest}`
  - `history.Scan(baseDir, loopName string, stepNames []string) ([]Entry, error)`
  - `history.Digest(dir, stepName string) (string, error)`

  Consumed by Task 6 (`tui/program.go` wiring) and Task 4 (`tui.HistorySnapshotMsg`/`DigestContentMsg` carry these types directly).

- [ ] **Step 1: Write the failing tests**

Create `history/history_test.go`:

```go
package history

import (
	"os"
	"path/filepath"
	"testing"
)

func writeIteration(t *testing.T, dir string, progressDone bool, lastEvent string, digestSteps ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "steps"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "context.json"), []byte(`{"vars":{}}`), 0o644); err != nil {
		t.Fatalf("write context.json: %v", err)
	}
	progress := `{"completed":[],"done":false}`
	if progressDone {
		progress = `{"completed":[],"done":true}`
	}
	if err := os.WriteFile(filepath.Join(dir, "progress.json"), []byte(progress), 0o644); err != nil {
		t.Fatalf("write progress.json: %v", err)
	}
	if lastEvent != "" {
		line := `{"step":"s","kind":"outcome","message":"` + lastEvent + `"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(line), 0o644); err != nil {
			t.Fatalf("write events.jsonl: %v", err)
		}
	}
	for _, name := range digestSteps {
		if err := os.WriteFile(filepath.Join(dir, "steps", name+".digest.md"), []byte("# "+name), 0o644); err != nil {
			t.Fatalf("write digest: %v", err)
		}
	}
}

func TestScan_FindsIterationsAcrossWorkerSubdirs(t *testing.T) {
	base := t.TempDir()
	writeIteration(t, filepath.Join(base, "runs", "loop1", "w1", "20260101T000000-001"), true, "advance", "get-tasks")
	writeIteration(t, filepath.Join(base, "runs", "loop1", "w2", "20260102T000000-001"), false, "no-work")

	entries, err := Scan(base, "loop1", []string{"get-tasks"})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].IterationID != "20260102T000000-001" {
		t.Errorf("entries[0].IterationID = %q, want newest first", entries[0].IterationID)
	}
	if entries[0].WorkerID != "w2" {
		t.Errorf("entries[0].WorkerID = %q, want w2", entries[0].WorkerID)
	}
	if entries[0].Status != "no-work" {
		t.Errorf("entries[0].Status = %q, want no-work", entries[0].Status)
	}
	if entries[1].Status != "done" {
		t.Errorf("entries[1].Status = %q, want done", entries[1].Status)
	}
	if !entries[1].Steps[0].HasDigest {
		t.Errorf("entries[1].Steps[0].HasDigest = false, want true")
	}
}

func TestScan_StatusRunningWhenNoTerminalEventOrDone(t *testing.T) {
	base := t.TempDir()
	writeIteration(t, filepath.Join(base, "runs", "loop1", "w1", "20260101T000000-001"), false, "")

	entries, err := Scan(base, "loop1", nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 1 || entries[0].Status != "running" {
		t.Fatalf("entries = %+v, want one running entry", entries)
	}
}

func TestScan_StatusAborted(t *testing.T) {
	base := t.TempDir()
	writeIteration(t, filepath.Join(base, "runs", "loop1", "w1", "20260101T000000-001"), false, "abort")

	entries, err := Scan(base, "loop1", nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 1 || entries[0].Status != "aborted" {
		t.Fatalf("entries = %+v, want one aborted entry", entries)
	}
}

func TestScan_NoWorkerSubdirLayout(t *testing.T) {
	base := t.TempDir()
	writeIteration(t, filepath.Join(base, "runs", "loop1", "20260101T000000-001"), true, "advance")

	entries, err := Scan(base, "loop1", nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].WorkerID != "" {
		t.Errorf("WorkerID = %q, want empty (no worker subdir)", entries[0].WorkerID)
	}
}

func TestScan_MissingRunsDirReturnsEmpty(t *testing.T) {
	base := t.TempDir()
	entries, err := Scan(base, "no-such-loop", nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestDigest_ReturnsContent(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "iter")
	writeIteration(t, dir, true, "advance", "get-tasks")

	content, err := Digest(dir, "get-tasks")
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if content != "# get-tasks" {
		t.Errorf("content = %q, want %q", content, "# get-tasks")
	}
}

func TestDigest_MissingFileReturnsEmptyNoError(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "iter")
	writeIteration(t, dir, true, "advance")

	content, err := Digest(dir, "no-such-step")
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if content != "" {
		t.Errorf("content = %q, want empty", content)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./history/... -v`
Expected: FAIL — package `history` doesn't exist yet.

- [ ] **Step 3: Implement the package**

Create `history/history.go`:

```go
// Package history scans looper's on-disk run directories to build a loop's
// run history: one entry per iteration, with per-step digest presence, for
// the fleet TUI's run-history browsing (see tui.Model's viewRuns/viewDigest).
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// StepDigest names one step of a loop, in config order, and whether the
// iteration captured a digest for it (steps/<name>.digest.md exists).
type StepDigest struct {
	Name      string
	HasDigest bool
}

// Entry describes one iteration of a loop's run history.
type Entry struct {
	Dir         string // absolute path to the iteration directory
	IterationID string // e.g. "20260717T134719-001"
	WorkerID    string // "" if the loop has no per-worker run subdirs
	Status      string // "running" | "done" | "aborted" | "no-work"
	Steps       []StepDigest
}

// Scan walks <baseDir>/runs/<loopName>/ for iteration directories (an
// iteration directory is any directory containing context.json; this
// transparently handles both the runs/<loop>/<iter> and
// runs/<loop>/<worker>/<iter> layouts) and returns one Entry per iteration
// found, newest first. stepNames is the loop's steps in config order; each
// is checked for a captured digest in every iteration found. A missing runs
// directory is not an error — it yields an empty slice.
func Scan(baseDir, loopName string, stepNames []string) ([]Entry, error) {
	root := filepath.Join(baseDir, "runs", loopName)
	var entries []Entry

	var walk func(dir string)
	walk = func(dir string) {
		list, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		hasContext := false
		for _, e := range list {
			if !e.IsDir() && e.Name() == "context.json" {
				hasContext = true
				break
			}
		}
		if hasContext {
			entries = append(entries, buildEntry(root, dir, stepNames))
			return
		}
		for _, e := range list {
			if e.IsDir() {
				walk(filepath.Join(dir, e.Name()))
			}
		}
	}
	walk(root)

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].IterationID > entries[j].IterationID
	})
	return entries, nil
}

func buildEntry(root, dir string, stepNames []string) Entry {
	workerID := ""
	if parent := filepath.Dir(dir); parent != root {
		workerID = filepath.Base(parent)
	}
	steps := make([]StepDigest, len(stepNames))
	for i, name := range stepNames {
		_, err := os.Stat(filepath.Join(dir, "steps", name+".digest.md"))
		steps[i] = StepDigest{Name: name, HasDigest: err == nil}
	}
	return Entry{
		Dir:         dir,
		IterationID: filepath.Base(dir),
		WorkerID:    workerID,
		Status:      status(dir),
		Steps:       steps,
	}
}

// status derives an iteration's status from progress.json's Done flag and
// the last "outcome" event in events.jsonl.
func status(dir string) string {
	done := false
	if data, err := os.ReadFile(filepath.Join(dir, "progress.json")); err == nil {
		var p struct {
			Done bool `json:"done"`
		}
		if json.Unmarshal(data, &p) == nil {
			done = p.Done
		}
	}
	switch lastOutcome(dir) {
	case "abort":
		return "aborted"
	case "no-work":
		return "no-work"
	}
	if done {
		return "done"
	}
	return "running"
}

// lastOutcome returns the Message of the last Kind=="outcome" event in
// dir/events.jsonl (runctx.Event's JSON shape), or "" if none is found.
func lastOutcome(dir string) string {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()

	var last string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Kind == "outcome" {
			last = ev.Message
		}
	}
	return last
}

// Digest reads the captured digest content for one step of the iteration at
// dir (an Entry.Dir from Scan). It returns "" with no error if the step has
// no digest for this iteration.
func Digest(dir, stepName string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "steps", stepName+".digest.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read digest: %w", err)
	}
	return string(data), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./history/... -v`
Expected: PASS, all `history` package tests green.

- [ ] **Step 5: Commit**

```bash
git add history/history.go history/history_test.go
git commit -m "feat: add history package for scanning run directories"
```

---

## Task 4: TUI — history/digest view state and navigation

**Files:**
- Modify: `tui/model.go` (viewKind consts, Model struct, Options struct, Update, handleKey, handleLoopRowKey)
- Test: `tui/model_test.go`

**Interfaces:**
- Consumes: `history.Entry`, `history.StepDigest` (Task 3).
- Produces:
  - `tui.HistorySnapshotMsg{LoopName string, Entries []history.Entry}`
  - `tui.DigestContentMsg{Step, Content string}`
  - `Options.LoadHistoryFn func(loopName string) tea.Cmd`
  - `Options.LoadDigestFn func(loopName string, entry history.Entry, step string) tea.Cmd`
  - `viewRuns`, `viewDigest` viewKind values

  Consumed by Task 5 (rendering) and Task 6 (`tui/program.go` wiring implements `LoadHistoryFn`/`LoadDigestFn`).

- [ ] **Step 1: Write the failing tests**

Add to `tui/model_test.go`:

```go
func TestHandleLoopRowKey_HTriggersLoadHistoryAndOpensViewRuns(t *testing.T) {
	var gotLoopName string
	m := NewModel(Options{
		LoadHistoryFn: func(loopName string) tea.Cmd {
			gotLoopName = loopName
			return func() tea.Msg { return nil }
		},
	})
	mm, _ := m.Update(LoopsSnapshotMsg{{Name: "loop1", Enabled: true}})
	m = mm.(Model)
	m.loopsFocused = true

	m, cmd := press(t, m, "h")
	if cmd == nil {
		t.Fatalf("expected a command from LoadHistoryFn")
	}
	if gotLoopName != "loop1" {
		t.Errorf("LoadHistoryFn called with %q, want loop1", gotLoopName)
	}
	if m.view != viewRuns {
		t.Errorf("view = %v, want viewRuns", m.view)
	}
	if m.historyLoop != "loop1" {
		t.Errorf("historyLoop = %q, want loop1", m.historyLoop)
	}
}

func TestHistorySnapshotMsg_PopulatesHistoryForMatchingLoop(t *testing.T) {
	m := NewModel(Options{})
	m.historyLoop = "loop1"

	entries := []history.Entry{{IterationID: "iter-1", Status: "done"}}
	mm, _ := m.Update(HistorySnapshotMsg{LoopName: "loop1", Entries: entries})
	m = mm.(Model)
	if len(m.history) != 1 || m.history[0].IterationID != "iter-1" {
		t.Errorf("history = %+v, want one entry iter-1", m.history)
	}
}

func TestHistorySnapshotMsg_IgnoredForStaleLoop(t *testing.T) {
	m := NewModel(Options{})
	m.historyLoop = "loop1"

	mm, _ := m.Update(HistorySnapshotMsg{LoopName: "loop2", Entries: []history.Entry{{IterationID: "iter-1"}}})
	m = mm.(Model)
	if len(m.history) != 0 {
		t.Errorf("history = %+v, want empty (stale loop name)", m.history)
	}
}

func TestViewRuns_EnterOpensViewDigest(t *testing.T) {
	m := NewModel(Options{})
	m.view = viewRuns
	m.historyLoop = "loop1"
	m.history = []history.Entry{{IterationID: "iter-1", Steps: []history.StepDigest{{Name: "a", HasDigest: true}}}}

	m, _ = press(t, m, "enter")
	if m.view != viewDigest {
		t.Errorf("view = %v, want viewDigest", m.view)
	}
	if m.selectedRun.IterationID != "iter-1" {
		t.Errorf("selectedRun = %+v, want iter-1", m.selectedRun)
	}
}

func TestViewDigest_EnterOnStepWithDigestCallsLoadDigestFn(t *testing.T) {
	var gotStep string
	m := NewModel(Options{
		LoadDigestFn: func(loopName string, entry history.Entry, step string) tea.Cmd {
			gotStep = step
			return func() tea.Msg { return nil }
		},
	})
	m.view = viewDigest
	m.historyLoop = "loop1"
	m.selectedRun = history.Entry{IterationID: "iter-1", Steps: []history.StepDigest{{Name: "a", HasDigest: true}}}

	_, cmd := press(t, m, "enter")
	if cmd == nil {
		t.Fatalf("expected a command from LoadDigestFn")
	}
	if gotStep != "a" {
		t.Errorf("LoadDigestFn called with step %q, want a", gotStep)
	}
}

func TestViewDigest_EnterOnStepWithoutDigestIsNoop(t *testing.T) {
	called := false
	m := NewModel(Options{
		LoadDigestFn: func(loopName string, entry history.Entry, step string) tea.Cmd {
			called = true
			return nil
		},
	})
	m.view = viewDigest
	m.selectedRun = history.Entry{Steps: []history.StepDigest{{Name: "a", HasDigest: false}}}

	press(t, m, "enter")
	if called {
		t.Errorf("LoadDigestFn should not be called for a step with no digest")
	}
}

func TestDigestContentMsg_PopulatesContent(t *testing.T) {
	m := NewModel(Options{})
	mm, _ := m.Update(DigestContentMsg{Step: "a", Content: "# hi"})
	m = mm.(Model)
	if m.digestStep != "a" || m.digestContent != "# hi" {
		t.Errorf("digestStep/digestContent = %q/%q, want a/# hi", m.digestStep, m.digestContent)
	}
}

func TestEsc_UnwindsViewDigestToViewRunsToViewFleet(t *testing.T) {
	m := NewModel(Options{})
	m.view = viewDigest
	m, _ = press(t, m, "esc")
	if m.view != viewRuns {
		t.Errorf("view = %v, want viewRuns", m.view)
	}
	m, _ = press(t, m, "esc")
	if m.view != viewFleet {
		t.Errorf("view = %v, want viewFleet", m.view)
	}
	if m.historyLoop != "" {
		t.Errorf("historyLoop = %q, want empty after leaving viewRuns", m.historyLoop)
	}
}
```

Add `"github.com/jbofill10/looper/history"` to `tui/model_test.go`'s import block.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./tui/... -run 'TestHandleLoopRowKey_H|TestHistorySnapshotMsg|TestViewRuns_|TestViewDigest_|TestDigestContentMsg|TestEsc_Unwinds' -v`
Expected: FAIL — `viewRuns`, `viewDigest`, `HistorySnapshotMsg`, `DigestContentMsg`, `LoadHistoryFn`, `LoadDigestFn`, `historyLoop`, `history`, `selectedRun`, `digestStep`, `digestContent` don't exist yet (compile errors).

- [ ] **Step 3: Add message types and Options fields**

In `tui/model.go`, add the import and new message types after `LoopsSnapshotMsg` (currently ending at line 80):

```go
	"github.com/jbofill10/looper/history"
```

(added to the existing import block, alongside `builder` and `style`)

```go
// HistorySnapshotMsg carries a loop's run history, as scanned from disk, in
// response to Options.LoadHistoryFn.
type HistorySnapshotMsg struct {
	LoopName string
	Entries  []history.Entry
}

// DigestContentMsg carries one step's captured digest markdown for the
// currently viewed iteration, in response to Options.LoadDigestFn.
type DigestContentMsg struct {
	Step    string
	Content string
}
```

In `Options` (currently lines 121-159), add after `DeleteLoopFn`:

```go
	// LoadHistoryFn fetches a loop's run history (scanned from its run
	// directory on disk), for the 'h' keybinding on a Loops-catalog row.
	LoadHistoryFn func(loopName string) tea.Cmd
	// LoadDigestFn fetches one step's captured digest content for a
	// specific run-history entry.
	LoadDigestFn func(loopName string, entry history.Entry, step string) tea.Cmd
```

- [ ] **Step 4: Add viewKind values and Model fields**

Change the `viewKind` const block (currently lines 164-168):

```go
const (
	viewFleet viewKind = iota
	viewFocus
	viewBuilder
	viewRuns
	viewDigest
)
```

Add to the `Model` struct (currently lines 173-197), after `loopBuilders`:

```go
	history       []history.Entry
	historyLoop   string // "" = not currently browsing any loop's history
	historyCursor int

	selectedRun   history.Entry
	digestCursor  int
	digestStep    string // name of the step DigestContentMsg last delivered content for
	digestContent string
```

- [ ] **Step 5: Wire the new messages into Update**

In `tui/model.go`'s `Update` (currently lines 246-293), add cases after `LoopsSnapshotMsg`'s case:

```go
	case HistorySnapshotMsg:
		if msg.LoopName == m.historyLoop {
			m.history = msg.Entries
			if m.historyCursor >= len(m.history) {
				m.historyCursor = len(m.history) - 1
			}
			if m.historyCursor < 0 {
				m.historyCursor = 0
			}
		}
		return m, nil
	case DigestContentMsg:
		m.digestStep = msg.Step
		m.digestContent = msg.Content
		return m, nil
```

- [ ] **Step 6: Extend up/down/enter/esc key handling**

In `tui/model.go`'s `handleKey` (currently lines 310-403), replace the `"up", "k"` case:

```go
	case "up", "k":
		switch m.view {
		case viewFleet:
			if m.loopsFocused {
				if m.treeCursor > 0 {
					m.treeCursor--
				}
			} else if m.cursor > 0 {
				m.cursor--
			}
		case viewRuns:
			if m.historyCursor > 0 {
				m.historyCursor--
			}
		case viewDigest:
			if m.digestCursor > 0 {
				m.digestCursor--
			}
		}
```

Replace the `"down", "j"` case:

```go
	case "down", "j":
		switch m.view {
		case viewFleet:
			if m.loopsFocused {
				if last := len(m.treeRows()) - 1; m.treeCursor < last {
					m.treeCursor++
				}
			} else if last := len(m.Workers()) - 1; m.cursor < last {
				m.cursor++
			}
		case viewRuns:
			if last := len(m.history) - 1; m.historyCursor < last {
				m.historyCursor++
			}
		case viewDigest:
			if last := len(m.selectedRun.Steps) - 1; m.digestCursor < last {
				m.digestCursor++
			}
		}
```

Replace the `"enter"` case:

```go
	case "enter":
		switch m.view {
		case viewFleet:
			if m.loopsFocused {
				break
			}
			rows := m.Workers()
			if m.cursor < len(rows) {
				row := rows[m.cursor]
				m.focusRun, m.focusWorker = row.RunID, row.WorkerID
				m.view = viewFocus
			}
		case viewRuns:
			if m.historyCursor < len(m.history) {
				m.selectedRun = m.history[m.historyCursor]
				m.digestCursor = 0
				m.digestStep = ""
				m.digestContent = ""
				m.view = viewDigest
			}
		case viewDigest:
			if m.digestCursor < len(m.selectedRun.Steps) {
				step := m.selectedRun.Steps[m.digestCursor]
				if step.HasDigest && m.opts.LoadDigestFn != nil {
					return m, m.opts.LoadDigestFn(m.historyLoop, m.selectedRun, step.Name)
				}
			}
		}
```

Replace the `"esc"` case:

```go
	case "esc":
		switch m.view {
		case viewDigest:
			m.view = viewRuns
		case viewRuns:
			m.view = viewFleet
			m.historyLoop = ""
			m.history = nil
		default:
			m.view = viewFleet
		}
```

Change the `"t", "o", "g", "R", "D"` case to include `"h"`:

```go
	case "t", "o", "g", "h", "R", "D":
		if m.view == viewFleet && m.loopsFocused {
			return m.handleLoopRowKey(msg.String())
		}
```

- [ ] **Step 7: Add the `h` action to `handleLoopRowKey`**

In `tui/model.go`'s `handleLoopRowKey` (currently lines 444-478), add a case after `"o"`:

```go
	case "h":
		if m.opts.LoadHistoryFn != nil {
			m.historyLoop = loop.Name
			m.history = nil
			m.historyCursor = 0
			m.view = viewRuns
			return m, m.opts.LoadHistoryFn(loop.Name)
		}
```

Also update the function's doc comment to mention `h` opens run history, alongside the existing t/o/g/x/R/D description.

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./tui/... -v`
Expected: PASS, all `tui` package tests green (this task doesn't yet implement `viewRuns()`/`viewDigest()` rendering, but `View()` still compiles and falls through to `viewFleet()` via its `default` case for the new viewKinds until Task 5 adds explicit cases).

- [ ] **Step 9: Commit**

```bash
git add tui/model.go tui/model_test.go
git commit -m "feat: add run-history and digest view navigation to fleet TUI"
```

---

## Task 5: TUI — render `viewRuns`/`viewDigest`, add glamour

**Files:**
- Modify: `tui/model.go` (`View()` dispatch, new `viewRuns`/`viewDigest` render functions), `go.mod`/`go.sum`
- Test: `tui/view_test.go`

**Interfaces:**
- Consumes: Model fields from Task 4 (`m.history`, `m.selectedRun`, `m.digestCursor`, `m.digestStep`, `m.digestContent`).
- Produces: rendered TUI output for the two new views. No new exported interfaces for other tasks.

- [ ] **Step 1: Add the glamour dependency**

Run: `go get github.com/charmbracelet/glamour`
Expected: `go.mod`/`go.sum` updated with `github.com/charmbracelet/glamour` and its transitive deps.

- [ ] **Step 2: Write the failing tests**

Add to `tui/view_test.go`:

```go
func TestViewRuns_ListsEntriesWithCursorAndStatus(t *testing.T) {
	m := NewModel(Options{})
	m.view = viewRuns
	m.historyLoop = "loop1"
	m.history = []history.Entry{
		{IterationID: "iter-2", WorkerID: "w1", Status: "done"},
		{IterationID: "iter-1", WorkerID: "w1", Status: "running"},
	}
	out := m.View()
	if !strings.Contains(out, "loop1") {
		t.Fatalf("viewRuns missing loop name:\n%s", out)
	}
	if !strings.Contains(out, "iter-2") || !strings.Contains(out, "iter-1") {
		t.Fatalf("viewRuns missing iteration rows:\n%s", out)
	}
	if !strings.Contains(out, "▸") {
		t.Fatalf("viewRuns missing cursor glyph:\n%s", out)
	}
}

func TestViewDigest_ListsStepsAndRendersLoadedContent(t *testing.T) {
	m := NewModel(Options{})
	m.view = viewDigest
	m.historyLoop = "loop1"
	m.selectedRun = history.Entry{
		IterationID: "iter-1",
		Steps: []history.StepDigest{
			{Name: "get-tasks", HasDigest: false},
			{Name: "plan", HasDigest: true},
		},
	}
	m.digestStep = "plan"
	m.digestContent = "# Plan\n\nDid the thing."

	out := m.View()
	if !strings.Contains(out, "get-tasks") || !strings.Contains(out, "plan") {
		t.Fatalf("viewDigest missing step names:\n%s", out)
	}
	if !strings.Contains(out, "Did the thing.") {
		t.Fatalf("viewDigest missing rendered digest content:\n%s", out)
	}
}

func TestViewDigest_NoDigestForSelectedStepShowsPlaceholder(t *testing.T) {
	m := NewModel(Options{})
	m.view = viewDigest
	m.selectedRun = history.Entry{
		Steps: []history.StepDigest{{Name: "get-tasks", HasDigest: false}},
	}
	out := m.View()
	if !strings.Contains(out, "no digest") {
		t.Fatalf("viewDigest missing no-digest placeholder:\n%s", out)
	}
}
```

Add `"github.com/jbofill10/looper/history"` to `tui/view_test.go`'s import block.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./tui/... -run 'TestViewRuns_|TestViewDigest_' -v`
Expected: FAIL — `View()` currently falls through to `viewFleet()` for `viewRuns`/`viewDigest`, so none of the expected content appears.

- [ ] **Step 4: Add `View()` dispatch cases**

In `tui/model.go`'s `View()` (currently lines 601-617), change the inner switch:

```go
	switch m.view {
	case viewFocus:
		return m.viewFocus()
	case viewBuilder:
		return m.viewBuilder()
	case viewRuns:
		return m.viewRuns()
	case viewDigest:
		return m.viewDigest()
	default:
		return m.viewFleet()
	}
```

- [ ] **Step 5: Implement `viewRuns()` and `viewDigest()`**

Add to `tui/model.go` (e.g. after `viewFocus`), along with the `glamour` import added to the file's import block:

```go
	"github.com/charmbracelet/glamour"
```

```go
// viewRuns renders the run-history list for m.historyLoop: one row per
// iteration found on disk, newest first, with a ▸ cursor on the selected
// row and a status glyph (running/done/aborted/no-work).
func (m Model) viewRuns() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.Title.Render(fmt.Sprintf("%s · run history", m.historyLoop)))
	if len(m.history) == 0 {
		b.WriteString("(no runs yet, or still loading)\n")
	}
	for i, e := range m.history {
		cursor := "  "
		if i == m.historyCursor {
			cursor = style.Marker.Render("▸ ")
		}
		worker := e.WorkerID
		if worker == "" {
			worker = "-"
		}
		fmt.Fprintf(&b, "%s%-24s %-8s %s\n", cursor, e.IterationID, worker, historyStatusGlyph(e.Status))
	}
	b.WriteString("\n" + style.KeyHint.Render("[up/down] move  [enter] view digests  [esc] back") + "\n")
	return b.String()
}

// historyStatusGlyph renders a run-history entry's status as a glyph +
// label, reusing the style package's existing glyph colors.
func historyStatusGlyph(status string) string {
	switch status {
	case "running":
		return style.GlyphRunning.Render("⚙ running")
	case "done":
		return style.GlyphDone.Render("✔ done")
	case "aborted":
		return style.GlyphNeedsYou.Render("✗ aborted")
	case "no-work":
		return style.GlyphEmpty.Render("∅ no-work")
	default:
		return status
	}
}

// viewDigest renders m.selectedRun's steps (in loop config order, with a
// marker for which captured a digest) and, once loaded via Options.
// LoadDigestFn, the currently selected step's rendered markdown digest.
func (m Model) viewDigest() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.Title.Render(fmt.Sprintf("%s · %s", m.historyLoop, m.selectedRun.IterationID)))

	for i, s := range m.selectedRun.Steps {
		cursor := "  "
		if i == m.digestCursor {
			cursor = style.Marker.Render("▸ ")
		}
		marker := " "
		if s.HasDigest {
			marker = "●"
		}
		line := fmt.Sprintf("%s%s %s", cursor, marker, s.Name)
		if !s.HasDigest {
			line = style.KeyHint.Render(line)
		}
		fmt.Fprintf(&b, "%s\n", line)
	}
	b.WriteString("\n")

	switch {
	case len(m.selectedRun.Steps) == 0:
		b.WriteString("(no steps)\n")
	case m.digestCursor >= len(m.selectedRun.Steps):
		// out of range; nothing to show
	case !m.selectedRun.Steps[m.digestCursor].HasDigest:
		b.WriteString(style.KeyHint.Render("(no digest for this step)") + "\n")
	case m.digestStep != m.selectedRun.Steps[m.digestCursor].Name:
		b.WriteString(style.KeyHint.Render("(press enter to load)") + "\n")
	default:
		rendered, err := glamour.Render(m.digestContent, "dark")
		if err != nil {
			rendered = m.digestContent
		}
		b.WriteString(rendered)
	}

	b.WriteString("\n" + style.KeyHint.Render("[up/down] move  [enter] load digest  [esc] back") + "\n")
	return b.String()
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./tui/... -v`
Expected: PASS, all `tui` package tests green.

- [ ] **Step 7: Commit**

```bash
git add tui/model.go go.mod go.sum tui/view_test.go
git commit -m "feat: render run-history and digest views in fleet TUI"
```

---

## Task 6: Wire history/digest loading into the live TUI

**Files:**
- Modify: `tui/program.go` (`Run`'s `Options` construction, new `loadHistoryFn`/`loadDigestFn` helpers)

**Interfaces:**
- Consumes: `history.Scan`, `history.Digest` (Task 3), `config.LoadLoopLenient` (existing), `Options.LoadHistoryFn`/`LoadDigestFn` (Task 4).
- Produces: a fully wired `h` keybinding end-to-end in the real (non-test) fleet TUI. Terminal task — no other task depends on this one's outputs.

- [ ] **Step 1: Add the wiring functions**

In `tui/program.go`, add the import:

```go
	"github.com/jbofill10/looper/history"
```

Add after `deleteLoopFn` (end of file):

```go
// loadHistoryFn returns the Options.LoadHistoryFn implementation: it loads
// loopName's step names from its loop file (to preserve config step order
// and label steps with no digest), scans its run directory on disk via
// history.Scan, and reports the result as a HistorySnapshotMsg.
func loadHistoryFn(baseDir string) func(loopName string) tea.Cmd {
	return func(loopName string) tea.Cmd {
		return func() tea.Msg {
			loop, err := config.LoadLoopLenient(filepath.Join(baseDir, "loops", loopName+".yaml"))
			if err != nil {
				return ErrMsg{Err: err}
			}
			stepNames := make([]string, len(loop.Steps))
			for i, s := range loop.Steps {
				stepNames[i] = s.Name
			}
			entries, err := history.Scan(baseDir, loopName, stepNames)
			if err != nil {
				return ErrMsg{Err: err}
			}
			return HistorySnapshotMsg{LoopName: loopName, Entries: entries}
		}
	}
}

// loadDigestFn returns the Options.LoadDigestFn implementation: it reads one
// step's captured digest content for a specific run-history entry via
// history.Digest and reports it as a DigestContentMsg.
func loadDigestFn() func(loopName string, entry history.Entry, step string) tea.Cmd {
	return func(loopName string, entry history.Entry, step string) tea.Cmd {
		return func() tea.Msg {
			content, err := history.Digest(entry.Dir, step)
			if err != nil {
				return ErrMsg{Err: err}
			}
			return DigestContentMsg{Step: step, Content: content}
		}
	}
}
```

- [ ] **Step 2: Wire the new Options fields**

In `tui/program.go`'s `Run` (currently lines 46-58), add to the `Options{...}` literal, after `DeleteLoopFn`:

```go
		LoadHistoryFn:      loadHistoryFn(baseDir),
		LoadDigestFn:       loadDigestFn(),
```

- [ ] **Step 3: Verify it builds**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 5: Commit**

```bash
git add tui/program.go
git commit -m "feat: wire run-history and digest loading into the fleet TUI"
```

---

## Final Verification

- [ ] Run `go build ./...` — expect success.
- [ ] Run `go test ./...` — expect all packages PASS.
- [ ] Manually exercise the feature per this repo's `verify`/`run` skills: start the daemon and TUI against a loop with a `digest`-bearing step (e.g. adapt `.looper/loops/cfa-dev-loop.yaml`'s `pick-repos` step to add `digest: REPOS_DIGEST_FILE`), run it once, then in the fleet TUI press `tab` to focus Loops, select the loop, press `h`, confirm the run-history list appears, press `enter` on an iteration, confirm the step list appears with a `●` marker on `pick-repos`, press `enter` on it, confirm the rendered digest markdown appears, and confirm `esc` unwinds view-by-view back to the fleet view.
