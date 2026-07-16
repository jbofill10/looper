# looper Milestone 8 — Guided Loop Builder

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Strict TDD. Checkbox steps.

**Goal:** A guided Bubble Tea builder to create/edit a loop — collect the loop name, concurrency, and a sequence of steps (type + type-specific fields), then write a validated `.looper/loops/<name>.yaml`. Entry: `looper new [name]` and `looper edit <name>`.

**Architecture:** `config.SaveLoop` writes a validated loop to YAML. A `builder.Model` (Bubble Tea) is a pure state machine that accumulates fields via key input (no `bubbles` dep — handle runes/enter/backspace directly, so it is fully unit-testable) and produces a `*config.Loop`. CLI commands launch it and save the result. Model logic is unit-tested by feeding key messages; the tea.Program run is manually verified.

**Tech Stack:** Go 1.26, `bubbletea`, existing deps. No new deps.

## Global Constraints

- Module `github.com/jbofill10/looper`; Go 1.26. PR into `main`. `go test -race ./...` clean.
- The builder must only ever emit loops that pass `config.Loop.Validate()`; `SaveLoop` re-validates before writing and returns an error otherwise.
- No network, no daemon — the builder is local file editing.

---

### Task 1: `config.SaveLoop`

**Files:** Modify `config/loop.go`; add `config/save_test.go`.

**Interface produced:**
- `func config.SaveLoop(l *Loop, path string) error` — `l.Validate()`; `yaml.Marshal`; `os.MkdirAll(filepath.Dir(path))`; write 0o644.
- Note the known `yaml.v3` block-scalar quirk: do NOT prefix multi-line `Run`/`Prompt` values with a leading newline. (Builder inputs won't, so this is fine; document it.)

**Test cases:** `SaveLoop` then `LoadLoop` round-trips a loop with a script step (multi-line run), a manual step, and outputs; an invalid loop (no steps) returns an error and writes nothing.

- [ ] TDD → commit `feat(config): SaveLoop writer`.

---

### Task 2: `builder.Model` state machine

**Files:** Create `builder/builder.go`; Test `builder/builder_test.go`.

**Interfaces produced:**
- `type Model struct { ... }` implementing `tea.Model`.
- `func New(existing *config.Loop) Model` — start a fresh builder, or pre-populate from an existing loop for editing.
- Stages (unexported enum): `stageLoopName → stageConcurrency → [per step: stageStepName → stageStepType → stage(type-specific) → stageStepOutputs → stageStepOnFail(script/headless only) → stageAddAnother] → stageDone`.
  - Type selection: typing one of `script|headless|interactive|manual` (validated; re-prompt on invalid).
  - Type-specific: `script`→`run`; `headless`/`interactive`→`prompt` then `harness` (blank ⇒ default); `manual`→nothing.
  - Outputs: comma-separated → `[]string` (trim, drop empties).
  - `on_fail`: `ask|retry|abort` (blank ⇒ ask).
  - Add another: `y`/`n`.
- Text entry handled in `Update` on `tea.KeyMsg`: printable runes append to the current field buffer, backspace deletes, enter commits the field and advances the stage.
- `func (m Model) Loop() (*config.Loop, bool)` — returns the assembled loop and whether the builder reached `stageDone`.
- `func (m Model) Done() bool`.
- On reaching `stageDone`, emit `tea.Quit`.

**Test cases (drive with synthetic key messages — a helper `typeString(m, s)` + `pressEnter(m)`):**
- Build a 2-step loop (script `get-task` with outputs `TASK_ID` + a `manual` review); assert `Loop()` returns a valid loop with the right names/types/fields and `Done()`.
- Invalid step type re-prompts (stage unchanged until a valid type entered).
- Concurrency parsing (numeric; blank ⇒ 1).
- Editing: `New(existing)` pre-populates name/concurrency so the produced loop preserves them unless changed.
- The produced loop passes `config.Loop.Validate()`.

- [ ] TDD → `go test ./builder/...` → commit `feat(builder): guided loop builder model`.

---

### Task 3: CLI `new` / `edit`

**Files:** Create `cli/build.go`; modify `cli/root.go`; Test `cli/build_test.go`.

**Commands:**
- `looper new [name]` — launch the builder (pre-seed the name if given); on completion, `config.SaveLoop` to `<cwd>/.looper/loops/<name>.yaml`; print the path. Non-TTY ⇒ hint + exit 0.
- `looper edit <name>` — `LoadLoop` the existing file, `builder.New(existing)`, save back. Error if the file is missing.
- A small testable core `func buildAndSave(m builder.Model, dir string) (path string, err error)` used after the program returns, so saving logic is unit-tested without a TTY.
- Register commands in `newRootCmd`.

**Test cases:** `buildAndSave` with a model already at `stageDone` (constructed by driving keys) writes the YAML and it re-loads via `LoadLoop`; non-TTY `looper new` prints a hint and exits 0 (built binary; `t.Skip` if no `go build`).

- [ ] TDD → `go build ./... && go test -race ./...` → commit `feat(cli): new and edit loop-builder commands`.

---

## Self-Review

- SaveLoop → Task 1. Builder model (pure, unit-tested) → Task 2. CLI new/edit → Task 3.
- Builder only emits validated loops; model logic fully unit-tested; tea.Program run manual. `go test -race` clean.
