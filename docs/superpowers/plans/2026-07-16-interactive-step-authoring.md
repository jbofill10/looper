# Interactive Step Authoring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the guided builder's field-by-field step editor with
`create-step`/`edit-step` keybinds that open a live `claude` session
(scoped to a looper-only plugin) which edits the loop's YAML file on disk
directly, for any step type.

**Architecture:** A new `harness`-embedded plugin (extracted to a cache
directory, loaded per-session via `--plugin-dir`) teaches `claude` the
full `Step` schema. A renamed `stepauthor` package (replacing `draft`)
launches that session via the existing `pty.Start`/`RunAttached`
primitives, pointed at the real loop file instead of a scratch script. The
`builder` package becomes a thin, file-backed list view: it reloads from
disk after every session, tracks per-step validation errors for red
display, and no longer assembles a `Loop` in memory to save once at the
end.

**Tech Stack:** Go 1.26, Bubble Tea (`charmbracelet/bubbletea`),
`gopkg.in/yaml.v3`, `creack/pty` (via the existing `pty` package).

## Global Constraints

- Module path `github.com/jbofill10/looper`, Go 1.26.
- `go build ./...` and `go test ./...` must pass after every task.
- Every change goes through a feature branch and a PR into `main` per this
  repo's `CLAUDE.md` — do not commit directly to `main`.
- The step-authoring session always resolves the `"claude"` harness
  directly (not a configurable harness name) — "start with claude for
  now" per the approved spec.
- No settings.json/`enabledPlugins` involved: `--plugin-dir <path>` alone
  activates a local plugin for one session (verified against Claude
  Code's plugin docs).

---

### Task 1: Per-step validation entry point (`config` package)

**Files:**
- Modify: `config/loop.go:96-150`
- Test: `config/loop_test.go` (append)

**Interfaces:**
- Produces: `func (s *Step) Validate() error` — checks `s` in isolation
  (name set, known type, script requires `run`, headless/interactive
  require `prompt`, `on_fail` is a known value or blank), and defaults
  `s.OnFail` to `OnFailAsk` when blank. Does not check cross-step
  concerns (duplicate names) — that stays in `Loop.Validate`.
- Consumes: nothing new; `knownType(t StepType) bool` already exists at
  `config/loop.go:96`.

- [ ] **Step 1: Write the failing test**

Append to `config/loop_test.go`:

```go
func TestStepValidate_Valid(t *testing.T) {
	cases := []Step{
		{Name: "a", Type: StepManual},
		{Name: "a", Type: StepScript, Run: "true"},
		{Name: "a", Type: StepHeadless, Prompt: "go"},
		{Name: "a", Type: StepInteractive, Prompt: "go"},
	}
	for _, s := range cases {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", s, err)
		}
	}
}

func TestStepValidate_DefaultsOnFail(t *testing.T) {
	s := Step{Name: "a", Type: StepScript, Run: "true"}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.OnFail != OnFailAsk {
		t.Errorf("on_fail = %q, want default %q", s.OnFail, OnFailAsk)
	}
}

func TestStepValidate_InvalidCases(t *testing.T) {
	cases := map[string]Step{
		"no name":            {Type: StepManual},
		"unknown type":       {Name: "a", Type: "bogus"},
		"script missing run": {Name: "a", Type: StepScript},
		"headless missing prompt": {Name: "a", Type: StepHeadless},
		"interactive missing prompt": {Name: "a", Type: StepInteractive},
		"bad on_fail": {Name: "a", Type: StepScript, Run: "true", OnFail: "explode"},
	}
	for label, s := range cases {
		t.Run(label, func(t *testing.T) {
			if err := s.Validate(); err == nil {
				t.Fatalf("expected error for %q, got nil", label)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./config/... -run TestStepValidate -v`
Expected: FAIL — `s.Validate undefined (type Step has no field or method Validate)`

- [ ] **Step 3: Implement `Step.Validate` and refactor `Loop.Validate` to use it**

Replace `config/loop.go:104-150` (the existing `Validate` method) with:

```go
// Validate checks s in isolation: name set, known type, required
// type-specific fields, and a known on_fail value, defaulting OnFail to
// OnFailAsk when blank. It does not check cross-step concerns like
// duplicate names within a loop — see Loop.Validate for that.
func (s *Step) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !knownType(s.Type) {
		return fmt.Errorf("unknown type %q", s.Type)
	}
	if s.Type == StepScript && s.Run == "" {
		return fmt.Errorf("script step requires 'run'")
	}
	if (s.Type == StepInteractive || s.Type == StepHeadless) && s.Prompt == "" {
		return fmt.Errorf("%s step requires 'prompt'", s.Type)
	}
	switch s.OnFail {
	case "", OnFailAsk, OnFailRetry, OnFailAbort:
	default:
		return fmt.Errorf("invalid on_fail %q", s.OnFail)
	}
	if s.OnFail == "" {
		s.OnFail = OnFailAsk
	}
	return nil
}

// Validate checks the loop for structural errors and fills in defaults.
func (l *Loop) Validate() error {
	if l.Name == "" {
		return fmt.Errorf("loop name is required")
	}
	if len(l.Steps) == 0 {
		return fmt.Errorf("loop must have at least one step")
	}
	if l.Concurrency == 0 {
		l.Concurrency = 1
	}
	if l.MaxConcurrency == 0 {
		l.MaxConcurrency = l.Concurrency
	}
	if l.TaskVar == "" {
		l.TaskVar = "TASK_ID"
	}
	seen := map[string]bool{}
	for i := range l.Steps {
		s := &l.Steps[i]
		if err := s.Validate(); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		if seen[s.Name] {
			return fmt.Errorf("duplicate step name %q", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}
```

- [ ] **Step 4: Run all config tests to verify they pass**

Run: `go test ./config/... -v`
Expected: PASS (all of `TestLoadLoop_*`, `TestValidate_*`, and the new
`TestStepValidate_*` cases)

- [ ] **Step 5: Commit**

```bash
git add config/loop.go config/loop_test.go
git commit -m "feat(config): add Step.Validate for per-step validation"
```

---

### Task 2: Bundled step-authoring plugin (`harness` package)

**Files:**
- Create: `harness/plugin/.claude-plugin/plugin.json`
- Create: `harness/plugin/skills/step-authoring/SKILL.md`
- Create: `harness/plugin.go`
- Delete: `harness/skill.go`
- Delete: `harness/skills/loop-creation/SKILL.md` (and the now-empty
  `harness/skills/` directory)
- Delete: `harness/skill_test.go`
- Test: `harness/plugin_test.go`

**Interfaces:**
- Produces: `const harness.StepAuthoringPluginName = "step-authoring"`,
  `func harness.EnsureStepAuthoringPlugin() (dir string, err error)`.
- Consumes: nothing new.

- [ ] **Step 1: Write the plugin manifest and skill content**

`harness/plugin/.claude-plugin/plugin.json`:

```json
{
  "name": "step-authoring",
  "description": "Looper's step-authoring skill, active only in looper-launched sessions."
}
```

`harness/plugin/skills/step-authoring/SKILL.md` (adapted from the
existing `harness/skills/loop-creation/SKILL.md`, broadened to cover
editing the loop file directly and the `manual` step type explicitly):

```markdown
---
name: step-authoring
description: Use when creating or editing a step in a looper loop's YAML file (.looper/loops/<name>.yaml). Looper is a Go CLI that runs an ordered list of steps as concurrent workers, looping. Covers all four step types, the step schema, the environment a script step runs in, and how outputs/failure-handling work.
---

# looper step authoring

You are editing a loop file directly: `.looper/loops/<name>.yaml`. A loop
is an ordered list of **steps**; each worker drives one work unit through
every step, then loops back to the first step for the next unit. Ask the
user what the step should do (or what they want changed, if editing an
existing one), then edit the YAML file yourself to match.

## Step schema (YAML fields, and the equivalent Go struct in
`config.Step`)

```yaml
- name: build          # required, unique within the loop
  type: script          # script | headless | interactive | manual
  run: ./scripts/build.sh   # script steps only: a shell command/script,
                             # run via `sh -c`
  prompt: "..."          # headless/interactive steps only: the prompt
                          # handed to the harness (may reference {{VAR}}
                          # tokens from earlier outputs)
  harness: claude         # headless/interactive steps only; blank = the
                          # global default harness
  outputs: [TASK_ID]      # names of variables this step may set (see
                          # "Setting outputs" below)
  on_fail: ask            # script/headless only: ask | retry | abort
  signals_no_work: false  # script steps only: see "Signaling no work"
```

## The `manual` step type

A `manual` step needs only `name` and `type: manual` — it's a pause point
where a human confirms something by hand before the worker continues. It
has no `run`, `prompt`, `harness`, `on_fail`, or meaningful `outputs`.

## Writing a script step's `run`

A script step's `run` is executed as `sh -c "<run>"` with:

- **Working directory**: the loop's workspace (the project checkout the
  worker is operating in).
- **Environment**: every variable currently in the run context (set by
  earlier steps' `outputs`, see below), plus `LOOPER_OUTPUT` — the path
  to a file the script can append `KEY=VALUE` lines to.
- **Exit code semantics**:
  - `0` → the step succeeded, the worker advances to the next step.
  - Non-zero → failure is handled per `on_fail`:
    - `ask` (default): a human is prompted to advance/retry/abort.
    - `retry`: the step re-runs.
    - `abort`: the worker's whole iteration aborts.
  - If `signals_no_work: true`, a specific exit code signals "no work
    available right now" rather than a failure.

Write scripts that are safe to retry (idempotent) since `on_fail: retry`
or a human choosing "retry" re-runs the same command with the same
environment.

### Setting outputs

To make a later step (or a later iteration) see a value this step
produces, append a `KEY=VALUE` line to the file at `$LOOPER_OUTPUT` for
every key declared in this step's `outputs:` list. Example:

```sh
task_id=$(pick-next-task)
echo "TASK_ID=$task_id" >> "$LOOPER_OUTPUT"
```

Only keys declared in `outputs:` are captured; anything else written to
that file is ignored. Once captured, `TASK_ID` (or whatever the key is)
becomes an environment variable for every subsequent step in the same
iteration, and a `{{TASK_ID}}`-style token for headless/interactive
prompts.

The variable that identifies "the work unit" for a loop defaults to
`TASK_ID` (configurable via the loop's `task_var`); the step that
acquires or picks the next unit of work should set it via `outputs`.

## headless / interactive steps

These hand a prompt to an agentic coding harness (e.g. `claude`) instead
of running a plain shell command:

- `headless` runs the harness non-interactively (`claude -p "<prompt>"`)
  and expects it to run to completion unattended.
- `interactive` hands the terminal to a live harness session; a human can
  watch/steer it, and the session's state (needs input, done, no work) is
  derived from sentinel strings the harness is expected to print.

Prompts may reference `{{VAR}}` tokens for any output variable set by an
earlier step (e.g. `{{TASK_ID}}`).

## Fixing a step that fails validation

If you're told a step currently fails validation (e.g. "interactive step
requires 'prompt'"), fix that specific problem first, then ask the user
if there's anything else they want changed about it.
```

- [ ] **Step 2: Write the failing test**

Create `harness/plugin_test.go`:

```go
package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureStepAuthoringPlugin_WritesManifestAndSkill(t *testing.T) {
	dir, err := EnsureStepAuthoringPlugin()
	if err != nil {
		t.Fatalf("EnsureStepAuthoringPlugin: %v", err)
	}

	manifestPath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	if !strings.Contains(string(data), `"name": "step-authoring"`) {
		t.Errorf("manifest missing expected name field, got:\n%s", data)
	}

	skillPath := filepath.Join(dir, "skills", "step-authoring", "SKILL.md")
	skillData, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("reading skill file: %v", err)
	}
	if !strings.HasPrefix(string(skillData), "---\nname: step-authoring") {
		t.Errorf("skill file missing expected frontmatter, got:\n%s", skillData[:min(80, len(skillData))])
	}
}

func TestEnsureStepAuthoringPlugin_OverwritesStaleContent(t *testing.T) {
	dir, err := EnsureStepAuthoringPlugin()
	if err != nil {
		t.Fatalf("EnsureStepAuthoringPlugin: %v", err)
	}
	manifestPath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	if err := os.WriteFile(manifestPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureStepAuthoringPlugin(); err != nil {
		t.Fatalf("EnsureStepAuthoringPlugin: %v", err)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) == "stale" {
		t.Errorf("expected the stale manifest to be refreshed")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./harness/... -run TestEnsureStepAuthoringPlugin -v`
Expected: FAIL — `undefined: EnsureStepAuthoringPlugin`

- [ ] **Step 4: Delete the old skill mechanism and implement the plugin**

Delete `harness/skill.go`, `harness/skill_test.go`, and the
`harness/skills/` directory (`rm -r harness/skills`).

Create `harness/plugin.go`:

```go
package harness

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// StepAuthoringPluginName is the plugin's name, as declared in
// plugin/.claude-plugin/plugin.json — the value a --plugin-dir-loaded
// session's skill is registered under.
const StepAuthoringPluginName = "step-authoring"

//go:embed plugin/.claude-plugin/plugin.json plugin/skills/step-authoring/SKILL.md
var stepAuthoringPlugin embed.FS

// stepAuthoringPluginFiles maps each embedded file to its path relative
// to the plugin directory root.
var stepAuthoringPluginFiles = map[string]string{
	"plugin/.claude-plugin/plugin.json":     filepath.Join(".claude-plugin", "plugin.json"),
	"plugin/skills/step-authoring/SKILL.md": filepath.Join("skills", "step-authoring", "SKILL.md"),
}

// EnsureStepAuthoringPlugin extracts looper's bundled step-authoring
// Claude Code plugin to a looper-owned cache directory (never inside the
// user's project, so ordinary claude sessions never discover it),
// refreshing it on every call, and returns the plugin directory written.
// A --plugin-dir-loaded session pointed at this directory activates the
// plugin for that session alone; nothing needs to touch enabledPlugins.
func EnsureStepAuthoringPlugin() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	dir := filepath.Join(cacheDir, "looper", "plugin")

	for embedPath, rel := range stepAuthoringPluginFiles {
		data, err := stepAuthoringPlugin.ReadFile(embedPath)
		if err != nil {
			return "", fmt.Errorf("read embedded %s: %w", embedPath, err)
		}
		dest := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", fmt.Errorf("create plugin directory: %w", err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", dest, err)
		}
	}
	return dir, nil
}
```

- [ ] **Step 5: Run harness tests to verify they pass**

Run: `go test ./harness/... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add harness/plugin.go harness/plugin_test.go harness/plugin/ \
        harness/skill.go harness/skill_test.go harness/skills/
git commit -m "feat(harness): bundle step-authoring as a local plugin, replacing loop-creation skill"
```

---

### Task 3: `harness.BuildStepAuthoring` argv builder

**Files:**
- Modify: `harness/harness.go` (add alongside `BuildHeadless`)
- Test: `harness/harness_test.go` (append)

**Interfaces:**
- Consumes: `config.Harness.Interactive []string` (existing).
- Produces: `func harness.BuildStepAuthoring(h config.Harness, prompt, pluginDir string) ([]string, error)`.

- [ ] **Step 1: Write the failing test**

Append to `harness/harness_test.go`:

```go
func TestBuildStepAuthoring(t *testing.T) {
	h := config.Harness{Interactive: []string{"claude"}}
	argv, err := BuildStepAuthoring(h, "do the thing", "/cache/looper/plugin")
	if err != nil {
		t.Fatalf("BuildStepAuthoring: %v", err)
	}
	want := []string{"claude", "--plugin-dir", "/cache/looper/plugin", "do the thing"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, argv[i], want[i])
		}
	}
}

func TestBuildStepAuthoring_NoInteractiveCommandErrors(t *testing.T) {
	if _, err := BuildStepAuthoring(config.Harness{}, "p", "/dir"); err == nil {
		t.Fatal("expected an error for a harness with no interactive command")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./harness/... -run TestBuildStepAuthoring -v`
Expected: FAIL — `undefined: BuildStepAuthoring`

- [ ] **Step 3: Implement it**

Add to `harness/harness.go` (near `BuildHeadless`):

```go
// BuildStepAuthoring returns h.Interactive with "--plugin-dir", pluginDir,
// and prompt appended, forming the argv for a step-authoring session
// (see stepauthor.CreateStep/EditStep). Unlike BuildInteractive, no
// --settings is involved: --plugin-dir alone activates a local plugin for
// the session. It errors if h.Interactive is empty.
func BuildStepAuthoring(h config.Harness, prompt, pluginDir string) ([]string, error) {
	if len(h.Interactive) == 0 {
		return nil, fmt.Errorf("harness has no interactive command configured")
	}
	argv := make([]string, len(h.Interactive), len(h.Interactive)+3)
	copy(argv, h.Interactive)
	argv = append(argv, "--plugin-dir", pluginDir, prompt)
	return argv, nil
}
```

- [ ] **Step 4: Run harness tests to verify they pass**

Run: `go test ./harness/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add harness/harness.go harness/harness_test.go
git commit -m "feat(harness): add BuildStepAuthoring argv builder"
```

---

### Task 4: `stepauthor` package, replacing `draft`

**Files:**
- Create: `stepauthor/stepauthor.go`
- Create: `stepauthor/stepauthor_test.go`
- Delete: `draft/draft.go`, `draft/draft_test.go` (and the now-empty
  `draft/` directory)

**Interfaces:**
- Consumes: `harness.EnsureStepAuthoringPlugin() (string, error)` (Task 2),
  `harness.BuildStepAuthoring(h config.Harness, prompt, pluginDir string) ([]string, error)`
  (Task 3), `pty.Start(pty.Config) (*pty.Session, error)`,
  `(*pty.Session).RunAttached(in, out *os.File) error` (existing, in
  package `pty`, imported as `looperpty` per repo convention).
- Produces:
  `func stepauthor.CreateStep(projectDir string, h config.Harness, loopPath string) error`
  `func stepauthor.EditStep(projectDir string, h config.Harness, loopPath, stepName string, validationErr error) error`

- [ ] **Step 1: Write the failing tests**

Create `stepauthor/stepauthor_test.go`:

```go
package stepauthor

import (
	"os"
	"testing"

	"github.com/jbofill10/looper/config"
	looperpty "github.com/jbofill10/looper/pty"
)

// skipIfNoPTY skips t if this environment cannot allocate a pty, mirroring
// runner and the former draft package's guard.
func skipIfNoPTY(t *testing.T) {
	t.Helper()
	probe, err := looperpty.Start(looperpty.Config{Argv: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Skipf("pty not available in this environment: %v", err)
	}
	_ = probe.Wait()
	_ = probe.Close()
}

func TestCreateStep_RunsSessionInProjectDir(t *testing.T) {
	skipIfNoPTY(t)
	dir := t.TempDir()

	// A fake "harness" that writes a known marker file into the project
	// directory it was started in, standing in for a real claude session
	// editing the loop file.
	h := config.Harness{Interactive: []string{
		"sh", "-c", `touch "$PWD"/create-step-ran`,
	}}

	if err := CreateStep(dir, h, dir+"/.looper/loops/x.yaml"); err != nil {
		t.Fatalf("CreateStep: %v", err)
	}
	if _, err := os.Stat(dir + "/create-step-ran"); err != nil {
		t.Errorf("expected session to run in %s: %v", dir, err)
	}
}

func TestEditStep_IncludesValidationErrorInPrompt(t *testing.T) {
	skipIfNoPTY(t)
	dir := t.TempDir()

	// Echo the prompt (claude's trailing positional arg) to a file so the
	// test can inspect what EditStep told the session.
	h := config.Harness{Interactive: []string{
		"sh", "-c", `printf '%s' "$1" > "$PWD"/prompt.txt`, "--",
	}}

	err := EditStep(dir, h, dir+"/.looper/loops/x.yaml", "deploy",
		fmt.Errorf("interactive step requires 'prompt'"))
	if err != nil {
		t.Fatalf("EditStep: %v", err)
	}

	got, err := os.ReadFile(dir + "/prompt.txt")
	if err != nil {
		t.Fatalf("reading prompt.txt: %v", err)
	}
	for _, want := range []string{"deploy", "interactive step requires 'prompt'"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("prompt = %q, want it to contain %q", got, want)
		}
	}
}

func TestCreateStep_NoInteractiveCommandErrors(t *testing.T) {
	dir := t.TempDir()
	if err := CreateStep(dir, config.Harness{}, dir+"/x.yaml"); err == nil {
		t.Fatal("expected an error for a harness with no interactive command")
	}
}
```

Add `"fmt"` and `"strings"` to this test file's imports alongside `"os"`
and `"testing"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./stepauthor/... -v`
Expected: FAIL — package `stepauthor` has no `CreateStep`/`EditStep`
(build failure, since the package doesn't exist yet)

- [ ] **Step 3: Delete `draft` and implement `stepauthor`**

Delete `draft/draft.go` and `draft/draft_test.go`, then `rmdir draft`.

Create `stepauthor/stepauthor.go`:

```go
// Package stepauthor launches a one-off, human-attended interactive
// claude session that creates or edits a step directly in a loop's YAML
// file on disk. Unlike runner.InteractiveExecutor, it has no hook socket
// or sentinel-derived state machine — it's just "run claude here, scoped
// to the step-authoring plugin, let the human collaborate with it, then
// wait for it to exit."
package stepauthor

import (
	"fmt"
	"os"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/harness"
	looperpty "github.com/jbofill10/looper/pty"
)

// CreateStep starts an interactive session of h in projectDir, prompting
// it to add a new step to the loop at loopPath, and attaches the local
// terminal to it until it exits.
func CreateStep(projectDir string, h config.Harness, loopPath string) error {
	prompt := fmt.Sprintf(
		"A new step needs to be added to the loop at %s. Ask the user what "+
			"the step should do, then use the step-authoring skill to add it "+
			"to the YAML correctly.", loopPath)
	return run(projectDir, h, prompt)
}

// EditStep starts an interactive session of h in projectDir, prompting it
// to edit the step named stepName in the loop at loopPath per the user's
// request. If validationErr is non-nil, its text is included so the
// session fixes that problem first.
func EditStep(projectDir string, h config.Harness, loopPath, stepName string, validationErr error) error {
	prompt := fmt.Sprintf(
		"Edit the step named %q in the loop at %s per the user's request. "+
			"Use the step-authoring skill.", stepName, loopPath)
	if validationErr != nil {
		prompt += fmt.Sprintf(
			" This step currently fails validation: %s. Fix that first, then "+
				"ask the user if there's anything else they want changed.",
			validationErr)
	}
	return run(projectDir, h, prompt)
}

// run ensures the step-authoring plugin is extracted, builds the session
// argv, starts it in projectDir, and attaches the local terminal to it
// until it exits.
func run(projectDir string, h config.Harness, prompt string) error {
	pluginDir, err := harness.EnsureStepAuthoringPlugin()
	if err != nil {
		return fmt.Errorf("install step-authoring plugin: %w", err)
	}

	argv, err := harness.BuildStepAuthoring(h, prompt, pluginDir)
	if err != nil {
		return err
	}

	sess, err := looperpty.Start(looperpty.Config{Argv: argv, Env: os.Environ(), Dir: projectDir})
	if err != nil {
		return fmt.Errorf("start step-authoring session: %w", err)
	}
	if err := sess.RunAttached(os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("step-authoring session: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run stepauthor tests to verify they pass**

Run: `go test ./stepauthor/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add stepauthor/ draft/
git commit -m "feat(stepauthor): replace draft package with file-editing CreateStep/EditStep"
```

---

### Task 5: Rewrite `builder` as a file-backed step list

**Files:**
- Modify: `builder/builder.go` (near-total rewrite)
- Modify: `builder/builder_test.go` (near-total rewrite)

**Interfaces:**
- Consumes: `config.LoadLoop(path string) (*Loop, error)`,
  `config.SaveLoop(l *Loop, path string) error`, `(*Step).Validate() error`
  (Task 1), `config.Loop`/`config.Step` (existing).
- Produces:
  - `type builder.AuthorRequest struct { ProjectDir, LoopPath, StepName string; ValidationErr error }`
    (`StepName == ""` means create; non-empty means edit)
  - `type builder.Options struct { AuthorFn func(AuthorRequest) tea.Cmd }`
  - `func builder.New(projectDir, loopPath string, opts Options) (Model, error)`
    — loads the file if it exists, or writes a `{name: <base of loopPath
    without .yaml>, steps: []}` skeleton first if it doesn't, then loads
    it; returns the load/save error if either fails.
  - `type builder.SessionDoneMsg struct { Err error }` (replacing
    `DraftedMsg`) — on receipt, the model reloads the file from disk and
    re-validates every step.
  - `func (m Model) Quit() bool` (replacing `Done()`, since there's no
    more "finished loop" — this just reports whether the user pressed the
    quit key) and `func (m Model) Path() string` (so callers can print
    where the loop lives; replaces `Loop()`).

This is a full rewrite of `builder/builder.go`'s contents (the package
doc comment, `fieldID` constants, and every `curX` field/`visibleFields`/
`addStep`/`finish`/`Loop`/`Done` are all removed).

- [ ] **Step 1: Write the failing tests**

Replace `builder/builder_test.go` entirely with:

```go
package builder

import (
	"os"
	"strings"
	"testing"

	"github.com/jbofill10/looper/config"

	tea "github.com/charmbracelet/bubbletea"
)

func press(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	mm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update did not return a builder.Model")
	}
	return mm
}

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func writeLoop(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dirOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return "."
	}
	return path[:i]
}

func TestNew_CreatesSkeletonWhenLoopFileMissing(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/fresh.yaml"

	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(m.Steps()) != 0 {
		t.Errorf("got %d steps, want 0 for a fresh skeleton", len(m.Steps()))
	}
	if _, err := os.Stat(loopPath); err != nil {
		t.Errorf("expected skeleton written at %s: %v", loopPath, err)
	}
}

func TestNew_LoadsExistingLoop(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: manual\n")

	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(m.Steps()) != 1 || m.Steps()[0].Name != "a" {
		t.Errorf("Steps() = %+v, want one step named a", m.Steps())
	}
}

func TestCreateStep_InvokesAuthorFnWithBlankStepName(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/fresh.yaml"
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	var gotReq AuthorRequest
	m.opts.AuthorFn = func(req AuthorRequest) tea.Cmd {
		gotReq = req
		return func() tea.Msg { return SessionDoneMsg{} }
	}

	m = press(t, m, key("c"))
	if gotReq.StepName != "" {
		t.Errorf("StepName = %q, want empty for create-step", gotReq.StepName)
	}
	if gotReq.LoopPath != loopPath {
		t.Errorf("LoopPath = %q, want %q", gotReq.LoopPath, loopPath)
	}
}

func TestEditStep_InvokesAuthorFnWithSelectedStepAndNoError(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: manual\n")
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	var gotReq AuthorRequest
	m.opts.AuthorFn = func(req AuthorRequest) tea.Cmd {
		gotReq = req
		return func() tea.Msg { return SessionDoneMsg{} }
	}

	m = press(t, m, key("e"))
	if gotReq.StepName != "a" {
		t.Errorf("StepName = %q, want a", gotReq.StepName)
	}
	if gotReq.ValidationErr != nil {
		t.Errorf("ValidationErr = %v, want nil for a valid step", gotReq.ValidationErr)
	}
}

func TestSessionDoneMsg_ReloadsAndFlagsInvalidStep(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: manual\n")
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the harness session having rewritten the file with an
	// invalid step before signaling done.
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: interactive\n")
	m = press(t, m, SessionDoneMsg{})

	errs := m.StepErrors()
	if errs["a"] == nil {
		t.Errorf("expected step %q to be flagged invalid after reload", "a")
	}
}

func TestEditStep_OnInvalidStepIncludesErrorInRequest(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: interactive\n")
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	var gotReq AuthorRequest
	m.opts.AuthorFn = func(req AuthorRequest) tea.Cmd {
		gotReq = req
		return func() tea.Msg { return SessionDoneMsg{} }
	}

	m = press(t, m, key("e"))
	if gotReq.ValidationErr == nil {
		t.Fatal("expected ValidationErr to be set for an invalid step")
	}
}

func TestDeleteStep_RewritesFileWithoutSession(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: manual\n  - name: b\n    type: manual\n")
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	m = press(t, m, key("d")) // deletes the selected (first) step, "a"

	loop, err := config.LoadLoop(loopPath)
	if err != nil {
		t.Fatalf("LoadLoop after delete: %v", err)
	}
	if len(loop.Steps) != 1 || loop.Steps[0].Name != "b" {
		t.Errorf("steps after delete = %+v, want only step b", loop.Steps)
	}
}

func TestQuit_SetOnQKey(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, dir+"/.looper/loops/x.yaml", Options{})
	if err != nil {
		t.Fatal(err)
	}
	m = press(t, m, key("q"))
	if !m.Quit() {
		t.Error("Quit() = false after pressing q, want true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./builder/... -v`
Expected: FAIL (build failure — none of `New`'s new signature, `Steps()`,
`AuthorRequest`, `SessionDoneMsg`, `StepErrors()`, `Quit()` exist yet)

- [ ] **Step 3: Implement the rewritten `builder` package**

Replace the entire contents of `builder/builder.go` with:

```go
// Package builder implements a file-backed Bubble Tea list view over a
// looper loop's steps. The loop's YAML file on disk is the source of
// truth at all times: Model reloads from it after every interactive
// authoring session and applies delete/reorder directly to it, rather
// than assembling a Loop in memory to save once at the end. The one
// side-effecting capability the view exposes — launching an interactive
// claude session to create or edit a step — is invoked via an injected
// Options.AuthorFn, mirroring how tui.Model injects RespondFn/AttachFn to
// stay pure itself.
package builder

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/style"
)

// AuthorRequest carries the context an Options.AuthorFn needs to launch a
// step-authoring session: the project directory and loop file path to run
// it against, which step to edit (blank means create a new one), and that
// step's current validation error, if any.
type AuthorRequest struct {
	ProjectDir    string
	LoopPath      string
	StepName      string
	ValidationErr error
}

// SessionDoneMsg reports that an authoring session invoked via
// Options.AuthorFn has exited (the user detached). Err is set only if the
// session itself failed to start or run (not for the edited file failing
// validation — that's surfaced via StepErrors after the reload Update
// performs on receipt of this message).
type SessionDoneMsg struct {
	Err error
}

// Options configures the builder's one side-effecting hook.
type Options struct {
	// AuthorFn, if set, is invoked when the user requests a step-authoring
	// session (create-step or edit-step). It returns a tea.Cmd that
	// performs the actual session and yields a SessionDoneMsg.
	AuthorFn func(AuthorRequest) tea.Cmd
}

// Model is the Bubble Tea model driving the file-backed step list.
type Model struct {
	opts Options

	projectDir string
	loopPath   string

	loop       *config.Loop
	stepErrors map[string]error

	cursor    int
	authoring bool
	quit      bool
	errMsg    string
}

// New loads the loop at loopPath, or, if it doesn't exist yet, writes a
// minimal skeleton there first (name derived from loopPath's base name,
// empty steps) via config.SaveLoop before loading it. projectDir is the
// working directory a step-authoring session is launched in.
func New(projectDir, loopPath string, opts Options) (Model, error) {
	loop, err := config.LoadLoop(loopPath)
	if err != nil {
		skeleton := &config.Loop{Name: loopName(loopPath), Steps: []config.Step{{Name: "placeholder", Type: config.StepManual}}}
		// A brand-new loop needs at least one step to pass Validate, so
		// SaveLoop can write it; create-step immediately lets the user
		// replace this placeholder.
		if saveErr := config.SaveLoop(skeleton, loopPath); saveErr != nil {
			return Model{}, fmt.Errorf("write new loop skeleton: %w", saveErr)
		}
		loop, err = config.LoadLoop(loopPath)
		if err != nil {
			return Model{}, fmt.Errorf("load newly written skeleton: %w", err)
		}
	}

	m := Model{
		opts:       opts,
		projectDir: projectDir,
		loopPath:   loopPath,
		loop:       loop,
	}
	m.revalidate()
	return m, nil
}

// loopName derives a loop's name from its file path: the base name
// without a .yaml/.yml extension.
func loopName(loopPath string) string {
	base := loopPath
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".yaml")
	base = strings.TrimSuffix(base, ".yml")
	return base
}

// Steps returns the current in-memory step list (as of the last load).
func (m Model) Steps() []config.Step {
	return m.loop.Steps
}

// StepErrors returns the current per-step validation errors, keyed by
// step name, as of the last reload.
func (m Model) StepErrors() map[string]error {
	return m.stepErrors
}

// Path returns the loop file path this model is backed by.
func (m Model) Path() string {
	return m.loopPath
}

// Quit reports whether the user has requested to leave the builder.
func (m Model) Quit() bool {
	return m.quit
}

// revalidate recomputes m.stepErrors from m.loop.Steps by calling
// (*config.Step).Validate() on each, independent of the others.
func (m *Model) revalidate() {
	errs := map[string]error{}
	for i := range m.loop.Steps {
		s := &m.loop.Steps[i]
		if err := s.Validate(); err != nil {
			errs[s.Name] = err
		}
	}
	m.stepErrors = errs
}

// Init implements tea.Model. The builder has no initial command.
func (m Model) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SessionDoneMsg:
		m.authoring = false
		if msg.Err != nil {
			m.errMsg = fmt.Sprintf("authoring session failed: %v", msg.Err)
			return m, nil
		}
		reloaded, err := config.LoadLoop(m.loopPath)
		if err != nil {
			// The file may be mid-edit and momentarily invalid; keep the
			// last good in-memory copy but surface the reload error.
			m.errMsg = fmt.Sprintf("reload after session: %v", err)
			return m, nil
		}
		m.loop = reloaded
		if m.cursor >= len(m.loop.Steps) {
			m.cursor = len(m.loop.Steps) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.revalidate()
		m.errMsg = ""
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey implements the step list's keyboard handling: up/k and
// down/j move the cursor, c requests a create-step session, e requests an
// edit-step session for the selected step, d deletes the selected step
// (rewriting the file directly, no session), and q requests to quit.
func (m Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.authoring {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.loop.Steps)-1 {
			m.cursor++
		}
		return m, nil
	case "c":
		return m.requestAuthor("")
	case "e":
		if len(m.loop.Steps) == 0 {
			return m, nil
		}
		return m.requestAuthor(m.loop.Steps[m.cursor].Name)
	case "d":
		return m.deleteSelected()
	case "q":
		m.quit = true
		return m, nil
	}
	return m, nil
}

// requestAuthor invokes Options.AuthorFn for a create (stepName == "") or
// edit (stepName set) session, including the selected step's current
// validation error for an edit request.
func (m Model) requestAuthor(stepName string) (tea.Model, tea.Cmd) {
	if m.opts.AuthorFn == nil || m.authoring {
		return m, nil
	}
	m.authoring = true
	m.errMsg = ""
	req := AuthorRequest{
		ProjectDir: m.projectDir,
		LoopPath:   m.loopPath,
		StepName:   stepName,
		ValidationErr: m.stepErrors[stepName],
	}
	return m, m.opts.AuthorFn(req)
}

// deleteSelected removes the selected step from the loop and saves it
// directly, with no authoring session involved.
func (m Model) deleteSelected() (tea.Model, tea.Cmd) {
	if len(m.loop.Steps) == 0 {
		return m, nil
	}
	next := append([]config.Step{}, m.loop.Steps[:m.cursor]...)
	next = append(next, m.loop.Steps[m.cursor+1:]...)
	updated := *m.loop
	updated.Steps = next
	if err := config.SaveLoop(&updated, m.loopPath); err != nil {
		m.errMsg = fmt.Sprintf("delete step: %v", err)
		return m, nil
	}
	m.loop = &updated
	if m.cursor >= len(m.loop.Steps) {
		m.cursor = len(m.loop.Steps) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.revalidate()
	m.errMsg = ""
	return m, nil
}

// View implements tea.Model, rendering the step list with the cursor and
// any invalid steps in red.
func (m Model) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.Title.Render(fmt.Sprintf("Loop: %s", m.loop.Name)))

	if len(m.loop.Steps) == 0 {
		b.WriteString(style.Label.Render("(no steps yet — press c to create one)") + "\n")
	}
	for i, s := range m.loop.Steps {
		marker := "  "
		if i == m.cursor {
			marker = style.Marker.Render("▸ ")
		}
		line := fmt.Sprintf("%s (%s)", s.Name, s.Type)
		if err, bad := m.stepErrors[s.Name]; bad {
			line = style.Error.Render(line + " — " + err.Error())
		}
		fmt.Fprintf(&b, "%s%s\n", marker, line)
	}

	if m.authoring {
		fmt.Fprintf(&b, "\n%s\n", style.Busy.Render("session running…"))
	}
	if m.errMsg != "" {
		fmt.Fprintf(&b, "\n%s\n", style.Error.Render("! "+m.errMsg))
	}
	b.WriteString("\n" + style.KeyHint.Render("[c] create-step  [e] edit-step  [d] delete  [↑/↓] move  [q] quit") + "\n")
	return b.String()
}
```

Note: the placeholder step `New` writes into a brand-new skeleton exists
solely so `config.SaveLoop`'s `Validate()` call succeeds (a loop needs
`≥1` step); it will show up as a normal `manual` step in the list until
the user deletes it or edits it into something real via `e`.

- [ ] **Step 4: Run builder tests to verify they pass**

Run: `go test ./builder/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add builder/builder.go builder/builder_test.go
git commit -m "feat(builder): rewrite as a file-backed step list with create/edit/delete keybinds"
```

---

### Task 6: Rewire `cli/build.go`

**Files:**
- Modify: `cli/build.go` (near-total rewrite)

**Interfaces:**
- Consumes: `builder.New(projectDir, loopPath string, opts builder.Options) (builder.Model, error)`,
  `builder.AuthorRequest`, `builder.SessionDoneMsg`, `(Model) Quit() bool`,
  `(Model) Path() string` (Task 5); `stepauthor.CreateStep`/`EditStep`
  (Task 4); `config.LoadGlobal`, `(*Global).ResolveHarness("claude")`
  (existing).
- Produces: same CLI surface (`looper new [name]`, `looper edit <name>`),
  now with no `buildAndSave`/`draftFn` — the file is already saved
  continuously by the builder itself.

- [ ] **Step 1: Replace `cli/build.go`'s contents**

```go
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jbofill10/looper/builder"
	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/stepauthor"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	tea "github.com/charmbracelet/bubbletea"
)

// runBuilder loads the global config, constructs the file-backed builder
// for loopPath (creating it if it doesn't exist), and runs it until the
// user quits.
func runBuilder(loopPath, wd string) (builder.Model, error) {
	global, err := config.LoadGlobal(globalPath())
	if err != nil {
		return builder.Model{}, fmt.Errorf("loading global config: %w", err)
	}

	var p *tea.Program
	m, err := builder.New(wd, loopPath, builder.Options{AuthorFn: authorFn(&p, global, wd)})
	if err != nil {
		return builder.Model{}, err
	}
	p = tea.NewProgram(m)

	final, err := p.Run()
	if err != nil {
		return builder.Model{}, fmt.Errorf("running builder: %w", err)
	}
	fm, ok := final.(builder.Model)
	if !ok {
		return builder.Model{}, fmt.Errorf("builder produced an unexpected model type")
	}
	return fm, nil
}

// authorFn returns the builder.Options.AuthorFn implementation for the
// standalone CLI builder: it releases the Bubble Tea program's hold on
// the terminal, runs a create/edit-step session via the stepauthor
// package against the "claude" harness, and restores the program's
// terminal control on return. pp captures the *tea.Program variable that
// runBuilder assigns after constructing it (authorFn is built before the
// Program exists, so it captures the variable, not its value).
func authorFn(pp **tea.Program, global *config.Global, wd string) func(builder.AuthorRequest) tea.Cmd {
	return func(req builder.AuthorRequest) tea.Cmd {
		return func() tea.Msg {
			p := *pp
			if p != nil {
				if err := p.ReleaseTerminal(); err != nil {
					return builder.SessionDoneMsg{Err: err}
				}
				defer p.RestoreTerminal()
			}

			h, err := global.ResolveHarness("claude")
			if err != nil {
				return builder.SessionDoneMsg{Err: err}
			}

			if req.StepName == "" {
				err = stepauthor.CreateStep(req.ProjectDir, h, req.LoopPath)
			} else {
				err = stepauthor.EditStep(req.ProjectDir, h, req.LoopPath, req.StepName, req.ValidationErr)
			}
			return builder.SessionDoneMsg{Err: err}
		}
	}
}

// notATerminal reports whether stdin or stdout is not a terminal, printing
// a hint to out when true. Launching an interactive Bubble Tea program on a
// non-terminal would hang forever (no terminal to read keys from or render
// into), so callers should skip straight to returning nil.
func notATerminal(cmd *cobra.Command) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(cmd.OutOrStdout(), "looper new/edit: stdin/stdout is not a terminal; run it from an interactive terminal.")
		return true
	}
	return false
}

// newNewCmd builds the `looper new <name>` subcommand, which opens the
// file-backed builder for <cwd>/.looper/loops/<name>.yaml, creating it
// first if it doesn't exist.
func newNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a new loop with the interactive builder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if notATerminal(cmd) {
				return nil
			}
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}
			loopPath := filepath.Join(wd, ".looper", "loops", args[0]+".yaml")

			fm, err := runBuilder(loopPath, wd)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", fm.Path())
			return nil
		},
	}
	return cmd
}

// newEditCmd builds the `looper edit <name>` subcommand, which opens the
// file-backed builder for an existing loop.
func newEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit an existing loop with the interactive builder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}
			loopPath := filepath.Join(wd, ".looper", "loops", args[0]+".yaml")
			if _, err := config.LoadLoop(loopPath); err != nil {
				return fmt.Errorf("loading loop %q: %w", args[0], err)
			}

			if notATerminal(cmd) {
				return nil
			}

			fm, err := runBuilder(loopPath, wd)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", fm.Path())
			return nil
		},
	}
	return cmd
}
```

Note `new`'s argument changed from optional (`cobra.MaximumNArgs(1)`) to
required (`cobra.ExactArgs(1)`): the old builder let you type the loop
name into a form field if omitted from the CLI arg; the new builder has
no name field, so the name must come from the file path up front.

- [ ] **Step 2: Build and run the `cli` package's tests**

Run: `go build ./... && go test ./cli/... -v`
Expected: PASS (fix any compile errors from the signature/argument
changes before moving on — there may be existing `cli` tests referencing
`buildAndSave`/`draftFn`/optional `new` args that need the equivalent
updates described above)

- [ ] **Step 3: Commit**

```bash
git add cli/build.go
git commit -m "feat(cli): wire new/edit commands to the file-backed builder and stepauthor"
```

---

### Task 7: Rewire `tui/model.go` and `tui/program.go`

**Files:**
- Modify: `tui/model.go`
- Modify: `tui/program.go`
- Modify: `tui/model_test.go` (wherever it constructs `Options{HarnessNames: ..., DraftFn: ...}` or calls `builder.New`/checks `.Done()`/`.Loop()` — update to the new `AuthorFn`/`Quit()`/`Path()` surface)

**Interfaces:**
- Consumes: `builder.New(projectDir, loopPath string, opts builder.Options) (builder.Model, error)`,
  `builder.AuthorRequest`, `builder.SessionDoneMsg`, `(Model) Quit() bool` (Task 5);
  `stepauthor.CreateStep`/`EditStep` (Task 4).
- Produces: `tui.Options` with `SaveLoopFn`, `HarnessNames`, `DraftFn`
  removed and `AuthorFn func(builder.AuthorRequest) tea.Cmd` added.

- [ ] **Step 1: Update `tui/model.go`'s `Options` and builder wiring**

In `tui/model.go`, replace the three fields at `model.go:116-126`:

```go
	// SaveLoopFn func(loop *config.Loop) (string, error)
	// HarnessNames []string
	// DraftFn func(builder.DraftRequest) tea.Cmd
```

with:

```go
	// AuthorFn, if set, is passed through to each builder.Model the
	// embedded builder constructs, letting it launch an interactive
	// claude session to create or edit a step (see
	// builder.Options.AuthorFn).
	AuthorFn func(builder.AuthorRequest) tea.Cmd
```

Update the `'n'` handler at `model.go:236-241` (constructing a fresh
builder when entering the builder view). Since `builder.New` now needs a
project directory and a loop path (and can error), `Options` needs a
`ProjectDir string` field alongside `AuthorFn` for the fleet TUI to
construct one, and pressing `'n'` needs a loop name. Simplest fit: prompt
for the name the same way the CLI does — require it be typed as part of
entering builder mode isn't in scope for this task's minimal viable
change, so keep `'n'`'s existing no-argument entry point but default to a
placeholder name the user immediately renames via edit-step... **this
plan does not attempt to redesign the fleet TUI's create-loop entry
point** (out of scope per the approved spec's non-goals, which only
covers step authoring, not loop creation UX). Instead, thread a
`NewLoopPathFn func() string` through `Options` that the embedding
program supplies (returning a fresh path such as
`.looper/loops/new-<n>.yaml` where `<n>` avoids collision), so `'n'`
becomes:

```go
	case "n":
		if m.view == viewFleet && m.opts.NewLoopPathFn != nil {
			loopPath := m.opts.NewLoopPathFn()
			b, err := builder.New(m.opts.ProjectDir, loopPath, builder.Options{AuthorFn: m.opts.AuthorFn})
			if err != nil {
				m.builderMsg = fmt.Sprintf("error: %v", err)
				return m, nil
			}
			m.builder = b
			m.builderMsg = ""
			m.view = viewBuilder
		}
```

Add `ProjectDir string` and `NewLoopPathFn func() string` to `Options`
alongside `AuthorFn`.

Update `handleBuilderKey` (`model.go:293-315`): it no longer checks
`m.builder.Done()`/calls `m.saveLoop` (the builder saves continuously
now) — it checks `m.builder.Quit()` instead and just switches views:

```go
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

	if m.builder.Quit() {
		m.builderMsg = fmt.Sprintf("saved %s", m.builder.Path())
		m.builder = builder.Model{}
		m.view = viewFleet
		return m, nil
	}

	return m, cmd
}
```

Remove the now-unused `saveLoop` method (`model.go:317-328`) and the
`"github.com/jbofill10/looper/config"` import if nothing else in the file
uses `config` directly (check with `goimports`/`go build` after editing).

Update the `case builder.DraftedMsg:` branch in `Update`
(`model.go:190-196`) to `case builder.SessionDoneMsg:` (same body,
forwarding to `m.builder.Update(msg)`).

- [ ] **Step 2: Update `tui/program.go`'s wiring**

Replace `program.go:41-46`'s `Options{...}` construction:

```go
	model := NewModel(Options{
		RespondFn:     respondFn(ctx, cl),
		AttachFn:      attachFn(ctx, cl, &p),
		ProjectDir:    wd,
		NewLoopPathFn: newLoopPathFn(wd),
		AuthorFn:      authorFn(&p, global, wd),
	})
```

Replace `program.go:72-100`'s `draftFn` function with:

```go
// authorFn returns the Options.AuthorFn implementation: it releases the
// Bubble Tea program's hold on the terminal (mirroring attachFn), runs a
// create/edit-step session via the stepauthor package against the
// "claude" harness, and restores the program's terminal control on
// return. pp captures the *tea.Program variable the same way attachFn
// does.
func authorFn(pp **tea.Program, global *config.Global, wd string) func(builder.AuthorRequest) tea.Cmd {
	return func(req builder.AuthorRequest) tea.Cmd {
		return func() tea.Msg {
			p := *pp
			if p != nil {
				if err := p.ReleaseTerminal(); err != nil {
					return builder.SessionDoneMsg{Err: err}
				}
				defer p.RestoreTerminal()
			}

			h, err := global.ResolveHarness("claude")
			if err != nil {
				return builder.SessionDoneMsg{Err: err}
			}

			if req.StepName == "" {
				err = stepauthor.CreateStep(req.ProjectDir, h, req.LoopPath)
			} else {
				err = stepauthor.EditStep(req.ProjectDir, h, req.LoopPath, req.StepName, req.ValidationErr)
			}
			return builder.SessionDoneMsg{Err: err}
		}
	}
}

// newLoopPathFn returns an Options.NewLoopPathFn implementation that picks
// an unused path under <wd>/.looper/loops/ each time it's called (new-1,
// new-2, ... skipping any that already exist), for the fleet TUI's 'n'
// (create loop) keybind.
func newLoopPathFn(wd string) func() string {
	return func() string {
		dir := filepath.Join(wd, ".looper", "loops")
		for i := 1; ; i++ {
			path := filepath.Join(dir, fmt.Sprintf("new-%d.yaml", i))
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return path
			}
		}
	}
}
```

Remove `program.go`'s now-unused `saveLoopFn` function
(`program.go:105-113`) and its `"github.com/jbofill10/looper/draft"`
import, replaced by `"github.com/jbofill10/looper/stepauthor"`.

- [ ] **Step 3: Update `tui` package tests**

Grep for the old surface and update each call site to match:

Run: `grep -rn "DraftFn\|SaveLoopFn\|HarnessNames\|builder\.DraftRequest\|\.Done()\|\.Loop()" tui/*_test.go`

For each hit, replace with the Task 5/6/7 equivalents (`AuthorFn`,
`builder.AuthorRequest`, `.Quit()`, `.Path()`), following the same
before/after shapes as the non-test wiring above — e.g. a test that built
`Options{HarnessNames: []string{"claude"}, DraftFn: fakeDraftFn}` and
asserted on `m.builder.Done()`/`m.builder.Loop()` becomes
`Options{ProjectDir: dir, NewLoopPathFn: func() string { return dir + "/.looper/loops/x.yaml" }, AuthorFn: fakeAuthorFn}`
asserting on `m.builder.Quit()`/`m.builder.Path()`.

- [ ] **Step 4: Build and test the whole module**

Run: `go build ./... && go test ./... -v`
Expected: PASS across every package

- [ ] **Step 5: Commit**

```bash
git add tui/model.go tui/program.go tui/model_test.go
git commit -m "feat(tui): wire fleet builder view to AuthorFn/stepauthor, drop SaveLoopFn"
```

---

### Task 8: Full-repo verification and stale-reference sweep

**Files:** none (verification only)

- [ ] **Step 1: Confirm no references to removed identifiers remain**

Run:
```bash
grep -rn "DraftFn\|DraftedMsg\|DraftRequest\|EnsureLoopCreationSkill\|loop-creation\|builder\.Loop()\|builder\.Done()\|package draft" --include="*.go" .
```
Expected: no output. If anything matches, fix that file before continuing
(most likely a stray doc comment or an unmigrated test helper).

- [ ] **Step 2: Full build and test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass, no vet warnings.

- [ ] **Step 3: Manual smoke test**

Run: `go run . new smoke-test` in a scratch directory (needs a real
terminal — run it directly, not through a non-interactive wrapper).
Confirm: the builder opens showing one placeholder step, pressing `c`
launches a live `claude` session in the same terminal scoped to the
step-authoring plugin (ask it to add a `manual` step named `check`, then
detach with the harness's own exit/quit), pressing `e` on a step reopens
a session, `d` deletes the selected step and the change is visible
immediately in `.looper/loops/smoke-test.yaml`, and `q` exits leaving the
file's last-saved state on disk.

- [ ] **Step 4: Commit any leftover fixups**

```bash
git add -A
git commit -m "chore: sweep stale references to the removed draft/DraftFn surface"
```

(Skip this commit if Step 1 found nothing and no fixups were needed.)
