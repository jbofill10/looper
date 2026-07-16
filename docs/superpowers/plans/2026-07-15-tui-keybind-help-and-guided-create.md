# TUI Keybind Help & In-Fleet Guided Loop Creation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user press `n` in looper's fleet TUI to launch the guided loop builder in-process (instead of quitting to run `looper new`), and keep the fleet footer's keybind hints accurate as this is added.

**Architecture:** Add a third `viewKind` (`viewBuilder`) to `tui.Model` that embeds a `builder.Model`. Key routing forks to a new `handleBuilderKey` while that view is active (`esc` cancels, `ctrl+c` quits, everything else forwards to the builder). When the builder reaches its done stage, the fleet `Model` extracts the loop and persists it via an injected `Options.SaveLoopFn` (matching the existing `RespondFn`/`AttachFn` purity pattern), then returns to the fleet view showing the outcome. `tui/program.go` wires the real `SaveLoopFn` using `os.Getwd()` + `config.SaveLoop`, mirroring `cli/build.go`'s `buildAndSave`.

**Tech Stack:** Go, `github.com/charmbracelet/bubbletea`, existing `github.com/jbofill10/looper/builder` and `github.com/jbofill10/looper/config` packages.

## Global Constraints

- `go build ./...` and `go test ./...` must pass after every task.
- Every change goes through a branch + PR into `main` (per repo `CLAUDE.md`); this plan is implemented on the already-created branch `docs/tui-keybind-help-and-guided-create-design`.
- `tui.Model` must stay pure — no direct file/network I/O in `model.go`; side effects go through injected `Options` function fields (`SaveLoopFn` follows `RespondFn`/`AttachFn`'s pattern exactly).
- The `builder` package (`builder/builder.go`) is not modified — cancellation (`esc`) is handled entirely at the `tui` layer, on top of the unchanged builder.
- Commit using Conventional Commits after each task.

---

### Task 1: Embed the guided builder in `tui.Model` as a new `viewBuilder` view

**Files:**
- Modify: `tui/model.go`
- Test: `tui/view_test.go`

**Interfaces:**
- Consumes: `builder.New(existing *config.Loop) builder.Model`, `(builder.Model) Update(tea.Msg) (tea.Model, tea.Cmd)`, `(builder.Model) View() string`, `(builder.Model) Done() bool`, `(builder.Model) Loop() (*config.Loop, bool)` — all pre-existing, unchanged (`builder/builder.go`).
- Produces: `Options.SaveLoopFn func(loop *config.Loop) (string, error)` — a new injected field later tasks (Task 2) wire up to real file I/O. Tests in this task use a fake.

- [ ] **Step 1: Write the failing tests for entering, cancelling, and completing the builder view**

Append to `tui/view_test.go` (add `"github.com/jbofill10/looper/config"` to the import block):

```go
// typeRunes feeds each character of s to m as individual key presses,
// simulating a user typing s into the current builder field.
func typeRunes(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = press(t, m, string(r))
	}
	return m
}

func TestView_NewLoopKeyEntersBuilder(t *testing.T) {
	m := twoWorkerModel()
	m, _ = press(t, m, "n")
	if m.view != viewBuilder {
		t.Fatalf("view = %v, want viewBuilder", m.view)
	}
	out := m.View()
	if !strings.Contains(out, "Loop name:") {
		t.Fatalf("builder view missing prompt:\n%s", out)
	}
	if !strings.Contains(out, "[esc] cancel") {
		t.Fatalf("builder view missing cancel hint:\n%s", out)
	}
}

func TestView_EscCancelsBuilderWithoutSaving(t *testing.T) {
	called := false
	m := NewModel(Options{
		SaveLoopFn: func(loop *config.Loop) (string, error) {
			called = true
			return "unused", nil
		},
	})
	m, _ = press(t, m, "n")
	m = typeRunes(t, m, "abandoned")
	m, _ = press(t, m, "esc")

	if m.view != viewFleet {
		t.Fatalf("view = %v, want viewFleet after esc", m.view)
	}
	if called {
		t.Fatalf("SaveLoopFn was called despite cancelling with esc")
	}
}

func TestView_CtrlCQuitsFromBuilder(t *testing.T) {
	m := NewModel(Options{})
	m, _ = press(t, m, "n")
	_, cmd := press(t, m, "ctrl+c")
	if cmd == nil {
		t.Fatalf("ctrl+c from builder view did not return a command")
	}
}

func TestView_CompletingBuilderSavesAndReturnsToFleet(t *testing.T) {
	var savedLoop *config.Loop
	m := NewModel(Options{
		SaveLoopFn: func(loop *config.Loop) (string, error) {
			savedLoop = loop
			return "/tmp/.looper/loops/" + loop.Name + ".yaml", nil
		},
	})

	m, _ = press(t, m, "n") // enter builder

	m = typeRunes(t, m, "dev-loop")
	m, _ = press(t, m, "enter") // name

	m, _ = press(t, m, "enter") // concurrency blank => 1

	m = typeRunes(t, m, "get-task")
	m, _ = press(t, m, "enter") // step name

	m = typeRunes(t, m, "manual")
	m, _ = press(t, m, "enter") // step type

	m, _ = press(t, m, "enter") // outputs blank

	m, _ = press(t, m, "enter") // add another? blank => no => builder done

	if m.view != viewFleet {
		t.Fatalf("view = %v, want viewFleet after builder completes", m.view)
	}
	if savedLoop == nil || savedLoop.Name != "dev-loop" {
		t.Fatalf("SaveLoopFn called with %+v, want loop named dev-loop", savedLoop)
	}
	if !strings.Contains(m.View(), "saved /tmp/.looper/loops/dev-loop.yaml") {
		t.Fatalf("fleet view missing save confirmation:\n%s", m.View())
	}
}

func TestView_FleetFooterMentionsNewLoopKey(t *testing.T) {
	m := twoWorkerModel()
	if !strings.Contains(m.View(), "[n] new loop") {
		t.Fatalf("fleet footer missing new-loop hint:\n%s", m.View())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail to compile (viewBuilder/SaveLoopFn don't exist yet)**

Run: `go test ./tui/... -run TestView_NewLoopKeyEntersBuilder -v`
Expected: build failure — `undefined: viewBuilder` (or `Options.SaveLoopFn`).

- [ ] **Step 3: Add the `viewBuilder` state, `Options.SaveLoopFn`, and key routing**

In `tui/model.go`, update the import block:

```go
import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jbofill10/looper/builder"
	"github.com/jbofill10/looper/config"
)
```

Add `SaveLoopFn` to the `Options` struct (after `AttachFn`, before `Quit`):

```go
	// SaveLoopFn persists a completed guided-builder loop and returns the
	// path written. Invoked when the embedded builder (viewBuilder) reaches
	// its done stage.
	SaveLoopFn func(loop *config.Loop) (string, error)
```

Add `viewBuilder` to the `viewKind` enum:

```go
const (
	viewFleet viewKind = iota
	viewFocus
	viewBuilder
)
```

Add fields to `Model` (after `focusRun, focusWorker string`):

```go
	builder    builder.Model
	builderMsg string
```

Change `Update` to route to the builder while `viewBuilder` is active:

```go
	case tea.KeyMsg:
		if m.view == viewBuilder {
			return m.handleBuilderKey(msg)
		}
		return m.handleKey(msg)
```

In `handleKey`, add a `"n"` case (alongside the existing `"esc"` case):

```go
	case "n":
		if m.view == viewFleet {
			m.builder = builder.New(nil)
			m.builderMsg = ""
			m.view = viewBuilder
		}
```

Add the new `handleBuilderKey` and `saveLoop` methods after `handleFocusKey`:

```go
// handleBuilderKey routes a key press while the guided loop builder
// (viewBuilder) is active: ctrl+c quits the whole program, esc discards
// the in-progress builder and returns to the fleet view without saving,
// and any other key is forwarded to the builder's own Update. If that
// forwarded key advances the builder to its done stage, the resulting
// loop is saved via Options.SaveLoopFn and the fleet view is shown with
// the outcome in builderMsg. The builder's own tea.Quit (it is designed
// to run standalone in the CLI's `looper new`) is swallowed here — it
// must not quit the embedding fleet program.
func (m Model) handleBuilderKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.builder = builder.Model{}
		m.view = viewFleet
		return m, nil
	}

	next, cmd := m.builder.Update(msg)
	m.builder = next.(builder.Model)

	if m.builder.Done() {
		loop, _ := m.builder.Loop()
		m.builderMsg = m.saveLoop(loop)
		m.builder = builder.Model{}
		m.view = viewFleet
		return m, nil
	}

	return m, cmd
}

// saveLoop persists loop via Options.SaveLoopFn and formats the outcome
// for display in the fleet view's builderMsg line.
func (m Model) saveLoop(loop *config.Loop) string {
	if m.opts.SaveLoopFn == nil {
		return ""
	}
	path, err := m.opts.SaveLoopFn(loop)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return fmt.Sprintf("saved %s", path)
}
```

Change `View` to dispatch on all three view kinds:

```go
func (m Model) View() string {
	switch m.view {
	case viewFocus:
		return m.viewFocus()
	case viewBuilder:
		return m.viewBuilder()
	default:
		return m.viewFleet()
	}
}
```

In `viewFleet`, print `builderMsg` under the header and add the `n` keybind to the footer:

```go
	var b strings.Builder
	fmt.Fprintf(&b, "looper · %d runs · %d NEED YOU\n\n", len(runs), m.NeedYouCount())
	if m.builderMsg != "" {
		fmt.Fprintf(&b, "%s\n\n", m.builderMsg)
	}
	for i, r := range rows {
		cursor := "  "
		if i == m.cursor {
			cursor = "▸ "
		}
		fmt.Fprintf(&b, "%s%-8s %-14s %-12s %s\n", cursor, r.WorkerID, r.Task, r.Step, glyph(r))
	}
	b.WriteString("\n[up/down] move  [enter] focus  [n] new loop  [q] quit\n")
	return b.String()
```

Add a new `viewBuilder` render method after `viewFocus`:

```go
// viewBuilder renders the embedded guided loop builder: its own
// prompt/input, plus a footer for the cancel/quit keys the builder
// package itself has no concept of (cancellation is a fleet-TUI-level
// concern layered on top of the unmodified builder).
func (m Model) viewBuilder() string {
	var b strings.Builder
	b.WriteString(m.builder.View())
	b.WriteString("\n[esc] cancel  [ctrl+c] quit\n")
	return b.String()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tui/... -v -run 'TestView_NewLoopKeyEntersBuilder|TestView_EscCancelsBuilderWithoutSaving|TestView_CtrlCQuitsFromBuilder|TestView_CompletingBuilderSavesAndReturnsToFleet|TestView_FleetFooterMentionsNewLoopKey'`
Expected: all `PASS`.

- [ ] **Step 5: Run the full test suite and build**

Run: `go build ./... && go test ./...`
Expected: all packages build and all tests `PASS` (in particular, no existing `tui` test broke — `TestView_QuitKeys`, `TestView_EnterSwitchesToFocus`, etc. still pass unchanged).

- [ ] **Step 6: Commit**

```bash
git add tui/model.go tui/view_test.go
git commit -m "feat(tui): embed guided loop builder as a new viewBuilder state"
```

---

### Task 2: Wire the real `SaveLoopFn` in `tui/program.go`

**Files:**
- Modify: `tui/program.go`
- Test: `tui/program_test.go`

**Interfaces:**
- Consumes: `Options.SaveLoopFn func(loop *config.Loop) (string, error)` (Task 1), `config.SaveLoop(l *config.Loop, path string) error` (pre-existing, `config/loop.go:79`).
- Produces: `saveLoopFn(dir string) func(loop *config.Loop) (string, error)` — an unexported helper other `tui` code does not need to call directly (only `Run` uses it), but is tested directly here.

- [ ] **Step 1: Write the failing tests for `saveLoopFn`**

Append to `tui/program_test.go` (add `"os"`, `"path/filepath"`, and `"github.com/jbofill10/looper/config"` to the import block):

```go
func TestSaveLoopFn_WritesUnderLoopsDir(t *testing.T) {
	dir := t.TempDir()
	fn := saveLoopFn(dir)

	loop := &config.Loop{
		Name:  "dev-loop",
		Steps: []config.Step{{Name: "step-1", Type: config.StepManual}},
	}

	path, err := fn(loop)
	if err != nil {
		t.Fatalf("saveLoopFn returned error: %v", err)
	}
	want := filepath.Join(dir, ".looper", "loops", "dev-loop.yaml")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %q: %v", path, err)
	}
}

func TestSaveLoopFn_InvalidLoopReturnsError(t *testing.T) {
	dir := t.TempDir()
	fn := saveLoopFn(dir)

	if _, err := fn(&config.Loop{}); err == nil {
		t.Fatalf("expected error for loop with no name/steps")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail to compile**

Run: `go test ./tui/... -run TestSaveLoopFn -v`
Expected: build failure — `undefined: saveLoopFn`.

- [ ] **Step 3: Implement `saveLoopFn` and wire it into `Run`**

In `tui/program.go`, update the import block:

```go
import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jbofill10/looper/client"
	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/rpc"
)
```

Change `Run` to resolve the working directory and pass `SaveLoopFn`:

```go
func Run(ctx context.Context, cl rpc.LooperClient, conn io.Closer) error {
	defer conn.Close()

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	var p *tea.Program
	model := NewModel(Options{
		RespondFn:  respondFn(ctx, cl),
		AttachFn:   attachFn(ctx, cl, &p),
		SaveLoopFn: saveLoopFn(wd),
	})
	p = tea.NewProgram(model)

	go sendRunsSnapshot(ctx, p, cl)
	go streamUpdates(ctx, p, cl)

	_, err = p.Run()
	return err
}
```

Add `saveLoopFn` after `Run`:

```go
// saveLoopFn returns the Options.SaveLoopFn implementation used by the
// running fleet TUI: it saves loop to <dir>/.looper/loops/<name>.yaml via
// config.SaveLoop, mirroring cli/build.go's buildAndSave (duplicated here
// rather than shared, since cli already imports tui and importing the
// other way would cycle).
func saveLoopFn(dir string) func(loop *config.Loop) (string, error) {
	return func(loop *config.Loop) (string, error) {
		path := filepath.Join(dir, ".looper", "loops", loop.Name+".yaml")
		if err := config.SaveLoop(loop, path); err != nil {
			return "", err
		}
		return path, nil
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tui/... -v -run TestSaveLoopFn`
Expected: both tests `PASS`.

- [ ] **Step 5: Run the full test suite and build**

Run: `go build ./... && go test ./...`
Expected: all packages build and all tests `PASS`.

- [ ] **Step 6: Commit**

```bash
git add tui/program.go tui/program_test.go
git commit -m "feat(tui): wire guided-builder loop saving into the running fleet TUI"
```

---

### Task 3: Open the PR

**Files:** none (repo process step, per `CLAUDE.md`'s workflow rule).

- [ ] **Step 1: Push the branch**

```bash
git push -u origin docs/tui-keybind-help-and-guided-create-design
```

- [ ] **Step 2: Open the PR into `main`**

```bash
gh pr create --base main --title "feat(tui): guided loop creation from the fleet view" --body "$(cat <<'EOF'
## Summary
- Add [n] new loop to the fleet TUI's footer and key handling
- Embed the existing guided loop builder as a new viewBuilder state (esc cancels, ctrl+c quits, completion saves via config.SaveLoop and returns to the fleet view)
- Add design spec at docs/superpowers/specs/2026-07-15-tui-keybind-help-and-guided-create-design.md

## Test plan
- [x] go build ./...
- [x] go test ./...
EOF
)"
```

## Self-Review Notes

- **Spec coverage:** Footer text (spec §1) → Task 1 Step 3 `viewFleet` change. `viewBuilder` state, `n` key, esc/ctrl+c routing, done→save→return (spec §2) → Task 1. `Options.SaveLoopFn` + `tui/program.go` wiring (spec §3) → Task 2. All spec sections have a task.
- **Placeholder scan:** none found — every step has literal code and exact commands.
- **Type consistency:** `Options.SaveLoopFn func(loop *config.Loop) (string, error)` is identical between its Task 1 declaration and Task 2's `saveLoopFn` construction and both test files. `viewBuilder` is used consistently as both the `viewKind` constant and the `Model.viewBuilder()` method name, matching the existing `viewFleet`/`viewFocus` pattern already in the file.
