# looper Milestone 1 — Core Loop Runner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A runnable `looper run <loop>` that executes a loop single-worker and in-process, supporting `script` and `manual` steps, with a persisted per-iteration run context and correct outcome/termination handling.

**Architecture:** Three isolated packages — `config` (loop YAML schema + validation), `runctx` (per-iteration run directory, KV context, persistence), and `runner` (worker loop, per-type step executors, outcome resolution) — wired together by a thin `cli` entrypoint. No daemon, no gRPC, no harness, no TUI yet; those arrive in later milestones. The worker loop, run-context format, and step-outcome model built here are the foundations every later milestone builds on, so they must be clean and well-tested.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` (config parsing), `github.com/spf13/cobra` (CLI), stdlib `testing`.

## Global Constraints

- Go module path: `github.com/jbofill/looper` (prefixes every import).
- Go version floor: 1.26 (matches installed toolchain).
- Per-iteration state lives under `.looper/runs/<loop>/<iteration-id>/`; loop definitions under `.looper/loops/<name>.yaml`. `.looper/runs/` is runtime state and must be git-ignored.
- Reserved script exit code `78` = "no work" (only honored when the step sets `signals_no_work: true`).
- Only `script` and `manual` step types are executable in this milestone. `interactive` and `headless` must parse and validate (forward-compatible loop files) but the runner rejects them with a clear "not supported until a later milestone" error.
- Minimize dependencies: no assertion library; use stdlib `testing` with `t.Fatalf`/`t.Errorf`.

---

### Task 1: Bootstrap module + `config` package (loop schema & validation)

**Files:**
- Create: `go.mod` (via `go mod init`)
- Create: `config/loop.go`
- Test: `config/loop_test.go`

**Interfaces:**
- Consumes: nothing (first task).
- Produces:
  - `config.StepType` (string) with consts `StepScript="script"`, `StepManual="manual"`, `StepInteractive="interactive"`, `StepHeadless="headless"`.
  - `config.OnFail` (string) with consts `OnFailAsk="ask"`, `OnFailRetry="retry"`, `OnFailAbort="abort"`.
  - `config.Step` struct (fields below).
  - `config.Loop` struct (fields below).
  - `func config.LoadLoop(path string) (*Loop, error)` — read + parse + validate.
  - `func (l *Loop) Validate() error`.

- [ ] **Step 1: Initialize the module and add the YAML dependency**

Run:
```bash
cd /home/dice/git/looper
go mod init github.com/jbofill/looper
go get gopkg.in/yaml.v3
```
Expected: `go.mod` created declaring `module github.com/jbofill/looper` and `go 1.26`; `gopkg.in/yaml.v3` added as a require.

- [ ] **Step 2: Write the failing test**

Create `config/loop_test.go`:
```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "loop.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func TestLoadLoop_Valid(t *testing.T) {
	p := writeTemp(t, `
name: dev-loop
concurrency: 1
max_iterations: 0
steps:
  - name: get-task
    type: script
    run: "echo TASK_ID=1 >> $LOOPER_OUTPUT"
    outputs: [TASK_ID]
    signals_no_work: true
  - name: review
    type: manual
`)
	loop, err := LoadLoop(p)
	if err != nil {
		t.Fatalf("LoadLoop: %v", err)
	}
	if loop.Name != "dev-loop" {
		t.Errorf("name = %q, want dev-loop", loop.Name)
	}
	if len(loop.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(loop.Steps))
	}
	if loop.Steps[0].Type != StepScript {
		t.Errorf("step0 type = %q, want script", loop.Steps[0].Type)
	}
	if !loop.Steps[0].SignalsNoWork {
		t.Errorf("step0 signals_no_work = false, want true")
	}
	if loop.Steps[1].Type != StepManual {
		t.Errorf("step1 type = %q, want manual", loop.Steps[1].Type)
	}
}

func TestLoadLoop_InvalidCases(t *testing.T) {
	cases := map[string]string{
		"no name":            "steps:\n  - name: a\n    type: manual\n",
		"no steps":           "name: x\nsteps: []\n",
		"unknown type":       "name: x\nsteps:\n  - name: a\n    type: bogus\n",
		"script missing run": "name: x\nsteps:\n  - name: a\n    type: script\n",
		"dup step names":     "name: x\nsteps:\n  - name: a\n    type: manual\n  - name: a\n    type: manual\n",
		"step missing name":  "name: x\nsteps:\n  - type: manual\n",
		"bad on_fail":        "name: x\nsteps:\n  - name: a\n    type: script\n    run: \"true\"\n    on_fail: explode\n",
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			if _, err := LoadLoop(writeTemp(t, body)); err == nil {
				t.Fatalf("expected error for %q, got nil", label)
			}
		})
	}
}

func TestValidate_DefaultsConcurrency(t *testing.T) {
	l := &Loop{Name: "x", Steps: []Step{{Name: "a", Type: StepManual}}}
	if err := l.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if l.Concurrency != 1 {
		t.Errorf("concurrency = %d, want default 1", l.Concurrency)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./config/...`
Expected: FAIL — `undefined: LoadLoop`, `undefined: Step`, etc.

- [ ] **Step 4: Write the implementation**

Create `config/loop.go`:
```go
// Package config defines looper's loop schema and validation.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// StepType identifies how a step is executed.
type StepType string

const (
	StepScript      StepType = "script"
	StepManual      StepType = "manual"
	StepInteractive StepType = "interactive"
	StepHeadless    StepType = "headless"
)

// OnFail is the policy applied when a script/headless step fails.
type OnFail string

const (
	OnFailAsk   OnFail = "ask"
	OnFailRetry OnFail = "retry"
	OnFailAbort OnFail = "abort"
)

// Step is one unit of work in a loop.
type Step struct {
	Name          string   `yaml:"name"`
	Type          StepType `yaml:"type"`
	Run           string   `yaml:"run,omitempty"`     // script
	Prompt        string   `yaml:"prompt,omitempty"`  // interactive/headless
	Harness       string   `yaml:"harness,omitempty"`
	Outputs       []string `yaml:"outputs,omitempty"`
	SignalsNoWork bool     `yaml:"signals_no_work,omitempty"`
	OnFail        OnFail   `yaml:"on_fail,omitempty"`
}

// Loop is an ordered list of steps run as a repeating workflow.
type Loop struct {
	Name           string `yaml:"name"`
	Concurrency    int    `yaml:"concurrency,omitempty"`
	MaxConcurrency int    `yaml:"max_concurrency,omitempty"`
	MaxIterations  int    `yaml:"max_iterations,omitempty"`
	Workspace      string `yaml:"workspace,omitempty"` // shared|worktree
	Steps          []Step `yaml:"steps"`
}

// LoadLoop reads, parses, and validates a loop definition file.
func LoadLoop(path string) (*Loop, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read loop file: %w", err)
	}
	var l Loop
	if err := yaml.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parse loop file: %w", err)
	}
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("invalid loop %q: %w", path, err)
	}
	return &l, nil
}

func knownType(t StepType) bool {
	switch t {
	case StepScript, StepManual, StepInteractive, StepHeadless:
		return true
	}
	return false
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
	seen := map[string]bool{}
	for i := range l.Steps {
		s := &l.Steps[i]
		if s.Name == "" {
			return fmt.Errorf("step %d: name is required", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("duplicate step name %q", s.Name)
		}
		seen[s.Name] = true
		if !knownType(s.Type) {
			return fmt.Errorf("step %q: unknown type %q", s.Name, s.Type)
		}
		if s.Type == StepScript && s.Run == "" {
			return fmt.Errorf("step %q: script step requires 'run'", s.Name)
		}
		if (s.Type == StepInteractive || s.Type == StepHeadless) && s.Prompt == "" {
			return fmt.Errorf("step %q: %s step requires 'prompt'", s.Name, s.Type)
		}
		switch s.OnFail {
		case "", OnFailAsk, OnFailRetry, OnFailAbort:
		default:
			return fmt.Errorf("step %q: invalid on_fail %q", s.Name, s.OnFail)
		}
		if s.OnFail == "" {
			s.OnFail = OnFailAsk
		}
	}
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./config/...`
Expected: PASS (all subtests).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum config/
git commit -m "feat(config): loop schema and validation"
```

---

### Task 2: `runctx` package (run directory, KV context, persistence)

**Files:**
- Create: `runctx/context.go`
- Test: `runctx/context_test.go`
- Create: `.gitignore`

**Interfaces:**
- Consumes: nothing from Task 1 (independent package).
- Produces:
  - `runctx.RunContext` struct: exported fields `Dir string` (JSON-ignored) and `Vars map[string]string` (JSON key `vars`).
  - `func runctx.New(dir string) (*RunContext, error)` — creates `dir`, `dir/artifacts`, `dir/steps`.
  - `func runctx.Load(dir string) (*RunContext, error)` — reads `dir/context.json`.
  - `func (rc *RunContext) Set(key, val string)`
  - `func (rc *RunContext) Get(key string) (string, bool)`
  - `func (rc *RunContext) Env() []string` — sorted `KEY=VALUE` slice.
  - `func (rc *RunContext) Save() error` — writes `dir/context.json`.
  - `func (rc *RunContext) ArtifactsDir() string`, `func (rc *RunContext) StepsDir() string`.
  - `func (rc *RunContext) AppendEvent(ev Event) error` — appends JSON line to `dir/events.jsonl`.
  - `func (rc *RunContext) WriteDigest(md string) error` — writes `dir/digest.md`.
  - `runctx.Event` struct: `Step string` (json `step`), `Kind string` (json `kind`), `Message string` (json `message,omitempty`).

- [ ] **Step 1: Write the failing test**

Create `runctx/context_test.go`:
```go
package runctx

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNew_CreatesDirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "iter1")
	rc, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, sub := range []string{"", "artifacts", "steps"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
			t.Errorf("expected dir %q: %v", sub, err)
		}
	}
	if rc.ArtifactsDir() != filepath.Join(dir, "artifacts") {
		t.Errorf("ArtifactsDir = %q", rc.ArtifactsDir())
	}
	if rc.StepsDir() != filepath.Join(dir, "steps") {
		t.Errorf("StepsDir = %q", rc.StepsDir())
	}
}

func TestSetGetEnv(t *testing.T) {
	rc, _ := New(filepath.Join(t.TempDir(), "i"))
	rc.Set("TASK_ID", "42")
	rc.Set("BRANCH", "feat/x")
	if v, ok := rc.Get("TASK_ID"); !ok || v != "42" {
		t.Errorf("Get(TASK_ID) = %q,%v", v, ok)
	}
	if _, ok := rc.Get("MISSING"); ok {
		t.Errorf("Get(MISSING) should be false")
	}
	want := []string{"BRANCH=feat/x", "TASK_ID=42"} // sorted
	if got := rc.Env(); !reflect.DeepEqual(got, want) {
		t.Errorf("Env() = %v, want %v", got, want)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "i")
	rc, _ := New(dir)
	rc.Set("A", "1")
	rc.Set("B", "2")
	if err := rc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded.Vars, rc.Vars) {
		t.Errorf("loaded vars = %v, want %v", loaded.Vars, rc.Vars)
	}
	if loaded.Dir != dir {
		t.Errorf("loaded.Dir = %q, want %q", loaded.Dir, dir)
	}
}

func TestAppendEvent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "i")
	rc, _ := New(dir)
	if err := rc.AppendEvent(Event{Step: "plan", Kind: "start"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := rc.AppendEvent(Event{Step: "plan", Kind: "done", Message: "ok"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	f, _ := os.Open(filepath.Join(dir, "events.jsonl"))
	defer f.Close()
	var count int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("bad json line: %v", err)
		}
		count++
	}
	if count != 2 {
		t.Errorf("event lines = %d, want 2", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./runctx/...`
Expected: FAIL — `undefined: New`, `undefined: Event`, etc.

- [ ] **Step 3: Write the implementation**

Create `runctx/context.go`:
```go
// Package runctx owns looper's per-iteration run directory: the KV context,
// artifacts, step logs, event log, and digest. It is written for every
// iteration regardless of what the steps do, so iteration history lives in one
// predictable place.
package runctx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RunContext is the per-iteration state store rooted at Dir.
type RunContext struct {
	Dir  string            `json:"-"`
	Vars map[string]string `json:"vars"`
}

// Event is one line in the iteration's events.jsonl.
type Event struct {
	Step    string `json:"step"`
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
}

// New creates the run directory and its artifacts/ and steps/ subdirs.
func New(dir string) (*RunContext, error) {
	for _, d := range []string{dir, filepath.Join(dir, "artifacts"), filepath.Join(dir, "steps")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return &RunContext{Dir: dir, Vars: map[string]string{}}, nil
}

// Load reads a previously saved run context from dir/context.json.
func Load(dir string) (*RunContext, error) {
	data, err := os.ReadFile(filepath.Join(dir, "context.json"))
	if err != nil {
		return nil, fmt.Errorf("read context.json: %w", err)
	}
	rc := &RunContext{Dir: dir, Vars: map[string]string{}}
	if err := json.Unmarshal(data, rc); err != nil {
		return nil, fmt.Errorf("parse context.json: %w", err)
	}
	if rc.Vars == nil {
		rc.Vars = map[string]string{}
	}
	return rc, nil
}

// Set stores a context variable.
func (rc *RunContext) Set(key, val string) { rc.Vars[key] = val }

// Get returns a context variable and whether it was present.
func (rc *RunContext) Get(key string) (string, bool) { v, ok := rc.Vars[key]; return v, ok }

// Env returns the context vars as a sorted slice of KEY=VALUE strings.
func (rc *RunContext) Env() []string {
	keys := make([]string, 0, len(rc.Vars))
	for k := range rc.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+rc.Vars[k])
	}
	return out
}

// ArtifactsDir is where steps drop produced files.
func (rc *RunContext) ArtifactsDir() string { return filepath.Join(rc.Dir, "artifacts") }

// StepsDir holds per-step logs and outputs files.
func (rc *RunContext) StepsDir() string { return filepath.Join(rc.Dir, "steps") }

// Save writes the KV context to dir/context.json.
func (rc *RunContext) Save() error {
	data, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context: %w", err)
	}
	return os.WriteFile(filepath.Join(rc.Dir, "context.json"), data, 0o644)
}

// AppendEvent appends one JSON-encoded event line to dir/events.jsonl.
func (rc *RunContext) AppendEvent(ev Event) error {
	f, err := os.OpenFile(filepath.Join(rc.Dir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open events log: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

// WriteDigest writes the iteration rollup to dir/digest.md.
func (rc *RunContext) WriteDigest(md string) error {
	return os.WriteFile(filepath.Join(rc.Dir, "digest.md"), []byte(md), 0o644)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./runctx/...`
Expected: PASS.

- [ ] **Step 5: Add .gitignore for runtime state**

Create `.gitignore`:
```
/looper
.looper/runs/
```

- [ ] **Step 6: Commit**

```bash
git add runctx/ .gitignore
git commit -m "feat(runctx): per-iteration run directory and KV context"
```

---

### Task 3: `runner` — outcomes, Prompter, and the script executor

**Files:**
- Create: `runner/runner.go` (shared types: `Outcome`, `Executor`, `Prompter`, fakes)
- Create: `runner/script.go` (`ScriptExecutor`)
- Test: `runner/script_test.go`

**Interfaces:**
- Consumes: `config.Step`, `config.OnFail*`, `config.StepScript` (Task 1); `runctx.RunContext` (Task 2).
- Produces:
  - `runner.Outcome` (int) with consts `OutcomeAdvance`, `OutcomeRetry`, `OutcomeAbort`, `OutcomeNoWork` (iota order).
  - `runner.NoWorkExitCode = 78`.
  - `runner.Executor` interface: `Run(rc *runctx.RunContext, step config.Step) (Outcome, error)`.
  - `runner.Prompter` interface: `AskFailure(step config.Step, exitCode int) (Outcome, error)` and `Manual(step config.Step) (Outcome, error)`.
  - `runner.FakePrompter` (test double) with settable `FailureOutcome`, `ManualOutcome`.
  - `runner.ScriptExecutor` struct with field `Prompter Prompter`; implements `Executor`.

- [ ] **Step 1: Write the failing test**

Create `runner/script_test.go`:
```go
package runner

import (
	"path/filepath"
	"testing"

	"github.com/jbofill/looper/config"
	"github.com/jbofill/looper/runctx"
)

func newRC(t *testing.T) *runctx.RunContext {
	t.Helper()
	rc, err := runctx.New(filepath.Join(t.TempDir(), "iter"))
	if err != nil {
		t.Fatalf("runctx.New: %v", err)
	}
	rc.Set("WORKDIR", t.TempDir())
	return rc
}

func TestScript_Success(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{Name: "ok", Type: config.StepScript, Run: "exit 0"}
	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
}

func TestScript_NoWork(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{Name: "gt", Type: config.StepScript, Run: "exit 78", SignalsNoWork: true}
	got, _ := exec.Run(rc, step)
	if got != OutcomeNoWork {
		t.Errorf("outcome = %v, want no-work", got)
	}
}

func TestScript_ExitNonZeroWithoutSignalIsFailure(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{Prompter: &FakePrompter{FailureOutcome: OutcomeAbort}}
	// 78 but NOT signals_no_work => treated as ordinary failure.
	step := config.Step{Name: "x", Type: config.StepScript, Run: "exit 78", OnFail: config.OnFailAsk}
	got, _ := exec.Run(rc, step)
	if got != OutcomeAbort {
		t.Errorf("outcome = %v, want abort (via prompter)", got)
	}
}

func TestScript_OnFailPolicies(t *testing.T) {
	rc := newRC(t)
	cases := []struct {
		policy config.OnFail
		want   Outcome
	}{
		{config.OnFailRetry, OutcomeRetry},
		{config.OnFailAbort, OutcomeAbort},
	}
	for _, c := range cases {
		exec := &ScriptExecutor{}
		step := config.Step{Name: "f", Type: config.StepScript, Run: "exit 1", OnFail: c.policy}
		got, err := exec.Run(rc, step)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got != c.want {
			t.Errorf("policy %q: outcome = %v, want %v", c.policy, got, c.want)
		}
	}
}

func TestScript_OnFailAskConsultsPrompter(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{Prompter: &FakePrompter{FailureOutcome: OutcomeRetry}}
	step := config.Step{Name: "f", Type: config.StepScript, Run: "exit 2", OnFail: config.OnFailAsk}
	got, _ := exec.Run(rc, step)
	if got != OutcomeRetry {
		t.Errorf("outcome = %v, want retry (from prompter)", got)
	}
}

func TestScript_CapturesDeclaredOutputs(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{
		Name:    "gt",
		Type:    config.StepScript,
		Run:     `printf 'TASK_ID=42\nIGNORED=zzz\n' >> "$LOOPER_OUTPUT"`,
		Outputs: []string{"TASK_ID"},
	}
	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if v, _ := rc.Get("TASK_ID"); v != "42" {
		t.Errorf("TASK_ID = %q, want 42", v)
	}
	if _, ok := rc.Get("IGNORED"); ok {
		t.Errorf("IGNORED should not be captured (not declared)")
	}
}

func TestScript_RunsInWorkdirWithContextEnv(t *testing.T) {
	rc := newRC(t)
	rc.Set("GREETING", "hi")
	exec := &ScriptExecutor{}
	step := config.Step{
		Name:    "env",
		Type:    config.StepScript,
		Run:     `echo "RESULT=$GREETING" >> "$LOOPER_OUTPUT"`,
		Outputs: []string{"RESULT"},
	}
	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if v, _ := rc.Get("RESULT"); v != "hi" {
		t.Errorf("RESULT = %q, want hi (context env not injected)", v)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./runner/...`
Expected: FAIL — `undefined: ScriptExecutor`, `undefined: OutcomeAdvance`, etc.

- [ ] **Step 3: Write the shared runner types**

Create `runner/runner.go`:
```go
// Package runner executes a loop's steps and drives the worker iteration loop.
package runner

import (
	"github.com/jbofill/looper/config"
	"github.com/jbofill/looper/runctx"
)

// NoWorkExitCode is the reserved script exit code meaning "no work available".
const NoWorkExitCode = 78

// Outcome is the result of running a step, deciding what the worker does next.
type Outcome int

const (
	OutcomeAdvance Outcome = iota // move to the next step
	OutcomeRetry                  // re-run the current step
	OutcomeAbort                  // stop this iteration
	OutcomeNoWork                 // no work available; stop the loop
)

func (o Outcome) String() string {
	switch o {
	case OutcomeAdvance:
		return "advance"
	case OutcomeRetry:
		return "retry"
	case OutcomeAbort:
		return "abort"
	case OutcomeNoWork:
		return "no-work"
	default:
		return "unknown"
	}
}

// Executor runs a single step and reports its outcome.
type Executor interface {
	Run(rc *runctx.RunContext, step config.Step) (Outcome, error)
}

// Prompter handles human interaction: manual steps and on_fail=ask decisions.
type Prompter interface {
	AskFailure(step config.Step, exitCode int) (Outcome, error)
	Manual(step config.Step) (Outcome, error)
}

// FakePrompter is a test double returning preset outcomes.
type FakePrompter struct {
	FailureOutcome Outcome
	ManualOutcome  Outcome
	FailureCalls   int
	ManualCalls    int
}

func (f *FakePrompter) AskFailure(step config.Step, exitCode int) (Outcome, error) {
	f.FailureCalls++
	return f.FailureOutcome, nil
}

func (f *FakePrompter) Manual(step config.Step) (Outcome, error) {
	f.ManualCalls++
	return f.ManualOutcome, nil
}
```

- [ ] **Step 4: Write the script executor**

Create `runner/script.go`:
```go
package runner

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jbofill/looper/config"
	"github.com/jbofill/looper/runctx"
)

// ScriptExecutor runs a shell command for a script step.
type ScriptExecutor struct {
	Prompter Prompter // consulted only for on_fail=ask
}

// Run executes step.Run via `sh -c` in the WORKDIR, with the run context vars
// injected as environment plus LOOPER_OUTPUT pointing at the step's outputs
// file. stdout+stderr are captured to steps/<name>.log.
func (e *ScriptExecutor) Run(rc *runctx.RunContext, step config.Step) (Outcome, error) {
	outPath := filepath.Join(rc.StepsDir(), step.Name+".outputs")
	logPath := filepath.Join(rc.StepsDir(), step.Name+".log")

	// Truncate any prior outputs file so a retry starts clean.
	if err := os.WriteFile(outPath, nil, 0o644); err != nil {
		return 0, fmt.Errorf("init outputs file: %w", err)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return 0, fmt.Errorf("create log: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command("sh", "-c", step.Run)
	if wd, ok := rc.Get("WORKDIR"); ok {
		cmd.Dir = wd
	}
	cmd.Env = append(os.Environ(), rc.Env()...)
	cmd.Env = append(cmd.Env, "LOOPER_OUTPUT="+outPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if !asExitError(runErr, &ee) {
			return 0, fmt.Errorf("run script %q: %w", step.Name, runErr)
		}
		exitCode = ee.ExitCode()
	}

	// Capture declared outputs regardless of exit code.
	if len(step.Outputs) > 0 {
		if err := captureOutputs(rc, step, outPath); err != nil {
			return 0, err
		}
	}

	if exitCode == 0 {
		return OutcomeAdvance, nil
	}
	if step.SignalsNoWork && exitCode == NoWorkExitCode {
		return OutcomeNoWork, nil
	}
	return e.resolveFailure(step, exitCode)
}

func (e *ScriptExecutor) resolveFailure(step config.Step, exitCode int) (Outcome, error) {
	switch step.OnFail {
	case config.OnFailRetry:
		return OutcomeRetry, nil
	case config.OnFailAbort:
		return OutcomeAbort, nil
	default: // OnFailAsk (validation defaults empty -> ask)
		if e.Prompter == nil {
			return OutcomeAbort, nil
		}
		return e.Prompter.AskFailure(step, exitCode)
	}
}

func captureOutputs(rc *runctx.RunContext, step config.Step, outPath string) error {
	f, err := os.Open(outPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open outputs: %w", err)
	}
	defer f.Close()

	declared := map[string]bool{}
	for _, k := range step.Outputs {
		declared[k] = true
	}
	found := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if declared[k] {
			found[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan outputs: %w", err)
	}
	for k, v := range found {
		rc.Set(k, v)
	}
	return nil
}

// asExitError reports whether err is an *exec.ExitError and, if so, assigns it.
func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./runner/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add runner/runner.go runner/script.go runner/script_test.go
git commit -m "feat(runner): outcomes, prompter, and script executor"
```

---

### Task 4: `runner` — manual executor + stdin prompter

**Files:**
- Create: `runner/manual.go` (`ManualExecutor`)
- Create: `runner/prompt.go` (`StdinPrompter`)
- Test: `runner/manual_test.go`

**Interfaces:**
- Consumes: `Prompter`, `Outcome*`, `Executor` (Task 3); `config.Step`, `config.StepManual` (Task 1); `runctx.RunContext` (Task 2).
- Produces:
  - `runner.ManualExecutor` struct with field `Prompter Prompter`; implements `Executor` by delegating to `Prompter.Manual`.
  - `runner.StdinPrompter` struct with fields `In io.Reader`, `Out io.Writer`; implements `Prompter` by reading a single-letter choice.

- [ ] **Step 1: Write the failing test**

Create `runner/manual_test.go`:
```go
package runner

import (
	"strings"
	"testing"

	"github.com/jbofill/looper/config"
)

func TestManual_DelegatesToPrompter(t *testing.T) {
	rc := newRC(t)
	fp := &FakePrompter{ManualOutcome: OutcomeAdvance}
	exec := &ManualExecutor{Prompter: fp}
	got, err := exec.Run(rc, config.Step{Name: "review", Type: config.StepManual})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
	if fp.ManualCalls != 1 {
		t.Errorf("ManualCalls = %d, want 1", fp.ManualCalls)
	}
}

func TestStdinPrompter_Manual(t *testing.T) {
	cases := map[string]Outcome{
		"a\n": OutcomeAdvance,
		"r\n": OutcomeRetry,
		"x\n": OutcomeAbort,
	}
	for input, want := range cases {
		p := &StdinPrompter{In: strings.NewReader(input), Out: &strings.Builder{}}
		got, err := p.Manual(config.Step{Name: "s", Type: config.StepManual})
		if err != nil {
			t.Fatalf("Manual(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("Manual(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestStdinPrompter_AskFailure(t *testing.T) {
	p := &StdinPrompter{In: strings.NewReader("r\n"), Out: &strings.Builder{}}
	got, err := p.AskFailure(config.Step{Name: "s"}, 1)
	if err != nil {
		t.Fatalf("AskFailure: %v", err)
	}
	if got != OutcomeRetry {
		t.Errorf("AskFailure = %v, want retry", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./runner/...`
Expected: FAIL — `undefined: ManualExecutor`, `undefined: StdinPrompter`.

- [ ] **Step 3: Write the manual executor**

Create `runner/manual.go`:
```go
package runner

import (
	"github.com/jbofill/looper/config"
	"github.com/jbofill/looper/runctx"
)

// ManualExecutor represents a human gate: it defers entirely to the Prompter.
type ManualExecutor struct {
	Prompter Prompter
}

func (e *ManualExecutor) Run(rc *runctx.RunContext, step config.Step) (Outcome, error) {
	return e.Prompter.Manual(step)
}
```

- [ ] **Step 4: Write the stdin prompter**

Create `runner/prompt.go`:
```go
package runner

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/jbofill/looper/config"
)

// StdinPrompter is the interactive terminal Prompter used by the CLI. It reads
// a single-letter choice: (a)dvance, (r)etry, (x)abort.
type StdinPrompter struct {
	In  io.Reader
	Out io.Writer
}

func (p *StdinPrompter) Manual(step config.Step) (Outcome, error) {
	fmt.Fprintf(p.Out, "Manual step %q. [a]dvance / [r]etry / [x]abort: ", step.Name)
	return p.readChoice()
}

func (p *StdinPrompter) AskFailure(step config.Step, exitCode int) (Outcome, error) {
	fmt.Fprintf(p.Out, "Step %q failed (exit %d). [a]dvance / [r]etry / [x]abort: ", step.Name, exitCode)
	return p.readChoice()
}

func (p *StdinPrompter) readChoice() (Outcome, error) {
	r := bufio.NewReader(p.In)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return OutcomeAbort, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "a", "advance", "":
		return OutcomeAdvance, nil
	case "r", "retry":
		return OutcomeRetry, nil
	case "x", "abort":
		return OutcomeAbort, nil
	default:
		return OutcomeAbort, nil
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./runner/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add runner/manual.go runner/prompt.go runner/manual_test.go
git commit -m "feat(runner): manual executor and stdin prompter"
```

---

### Task 5: `runner` — the worker iteration loop

**Files:**
- Create: `runner/worker.go` (`Worker`)
- Test: `runner/worker_test.go`

**Interfaces:**
- Consumes: `config.Loop`, `config.Step`, step types (Task 1); `runctx` (Task 2); `Executor`, `Outcome*`, `Prompter`, `ScriptExecutor`, `ManualExecutor` (Tasks 3–4).
- Produces:
  - `runner.Worker` struct with exported fields:
    - `Loop *config.Loop`
    - `BaseDir string` (the `.looper` dir; runs go under `BaseDir/runs/<loop>/<id>`)
    - `Workdir string` (execution dir for `workspace: shared`; set as `WORKDIR` var each iteration)
    - `Prompter Prompter`
    - `NewID func() string` (iteration id generator; injectable for tests)
  - `func (w *Worker) Run() error` — runs iterations until no-work / max_iterations / abort.
  - `func (w *Worker) executorFor(step config.Step) (Executor, error)` (unexported; rejects interactive/headless).

- [ ] **Step 1: Write the failing test**

Create `runner/worker_test.go`:
```go
package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jbofill/looper/config"
)

// idSeq returns a deterministic id generator: "iter-1", "iter-2", ...
func idSeq() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("iter-%d", n) }
}

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

func TestWorker_RunsUntilNoWork(t *testing.T) {
	// get-task exits 78 (no work) on the *second* iteration by using a counter file.
	counter := filepath.Join(t.TempDir(), "n")
	loop := &config.Loop{
		Name: "l",
		Steps: []config.Step{
			{
				Name: "get-task", Type: config.StepScript, SignalsNoWork: true,
				Run: fmt.Sprintf(`n=$(cat %q 2>/dev/null || echo 0); n=$((n+1)); echo $n > %q; [ $n -ge 2 ] && exit 78; echo TASK_ID=$n >> "$LOOPER_OUTPUT"`, counter, counter),
				Outputs: []string{"TASK_ID"},
			},
			{Name: "work", Type: config.StepScript, Run: `echo "did $TASK_ID"`},
		},
	}
	if err := loop.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	w := newWorker(t, loop, &FakePrompter{})
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Iteration 1 ran fully; iteration 2 hit no-work at get-task and stopped.
	if _, err := os.Stat(filepath.Join(w.BaseDir, "runs", "l", "iter-1")); err != nil {
		t.Errorf("expected iter-1 run dir: %v", err)
	}
}

func TestWorker_MaxIterations(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 2,
		Steps:         []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, id := range []string{"iter-1", "iter-2"} {
		if _, err := os.Stat(filepath.Join(w.BaseDir, "runs", "l", id)); err != nil {
			t.Errorf("expected %s: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(w.BaseDir, "runs", "l", "iter-3")); err == nil {
		t.Errorf("iter-3 should not exist (max_iterations=2)")
	}
}

func TestWorker_OutputsFlowBetweenSteps(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps: []config.Step{
			{Name: "produce", Type: config.StepScript, Run: `echo TASK_ID=99 >> "$LOOPER_OUTPUT"`, Outputs: []string{"TASK_ID"}},
			{Name: "consume", Type: config.StepScript, Run: `echo "TASK=$TASK_ID" >> "$LOOPER_OUTPUT"`, Outputs: []string{"TASK"}},
		},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	log := filepath.Join(w.BaseDir, "runs", "l", "iter-1", "steps", "consume.outputs")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read consume outputs: %v", err)
	}
	if !strings.Contains(string(data), "TASK=99") {
		t.Errorf("consume did not see TASK_ID from produce; got %q", data)
	}
}

func TestWorker_AbortStopsIteration(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps: []config.Step{
			{Name: "boom", Type: config.StepScript, Run: "exit 1", OnFail: config.OnFailAbort},
			{Name: "never", Type: config.StepScript, Run: `echo ran >> "$LOOPER_OUTPUT"`},
		},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.BaseDir, "runs", "l", "iter-1", "steps", "never.log")); err == nil {
		t.Errorf("second step should not have run after abort")
	}
}

func TestWorker_RejectsInteractive(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "plan", Type: config.StepInteractive, Prompt: "hi"}},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	err := w.Run()
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected 'not supported' error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./runner/...`
Expected: FAIL — `undefined: Worker`.

- [ ] **Step 3: Write the worker**

Create `runner/worker.go`:
```go
package runner

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jbofill/looper/config"
	"github.com/jbofill/looper/runctx"
)

// Worker drives one loop's iterations single-threaded, in-process.
type Worker struct {
	Loop     *config.Loop
	BaseDir  string // the .looper dir
	Workdir  string // execution dir (workspace: shared)
	Prompter Prompter
	NewID    func() string
}

func (w *Worker) idGen() func() string {
	if w.NewID != nil {
		return w.NewID
	}
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("%s-%03d", time.Now().UTC().Format("20060102T150405"), n)
	}
}

// Run executes iterations until get-task signals no-work, max_iterations is
// reached, or a step aborts the loop.
func (w *Worker) Run() error {
	gen := w.idGen()
	for iter := 1; w.Loop.MaxIterations == 0 || iter <= w.Loop.MaxIterations; iter++ {
		id := gen()
		stop, err := w.runIteration(id)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// runIteration runs all steps for one work unit. It returns stop=true when the
// loop should end (no-work signalled).
func (w *Worker) runIteration(id string) (stop bool, err error) {
	dir := filepath.Join(w.BaseDir, "runs", w.Loop.Name, id)
	rc, err := runctx.New(dir)
	if err != nil {
		return false, err
	}
	rc.Set("WORKDIR", w.Workdir)

	var digest strings.Builder
	fmt.Fprintf(&digest, "# Iteration %s\n\n", id)

	i := 0
	for i < len(w.Loop.Steps) {
		step := w.Loop.Steps[i]
		exec, err := w.executorFor(step)
		if err != nil {
			return false, err
		}
		_ = rc.AppendEvent(runctx.Event{Step: step.Name, Kind: "start"})
		outcome, err := exec.Run(rc, step)
		if err != nil {
			return false, fmt.Errorf("step %q: %w", step.Name, err)
		}
		_ = rc.AppendEvent(runctx.Event{Step: step.Name, Kind: "outcome", Message: outcome.String()})
		fmt.Fprintf(&digest, "- %s → %s\n", step.Name, outcome)

		if err := rc.Save(); err != nil {
			return false, err
		}

		switch outcome {
		case OutcomeAdvance:
			i++
		case OutcomeRetry:
			// stay on the same step
		case OutcomeAbort:
			_ = rc.WriteDigest(digest.String())
			return false, nil
		case OutcomeNoWork:
			_ = rc.WriteDigest(digest.String())
			return true, nil
		}
	}
	return false, rc.WriteDigest(digest.String())
}

func (w *Worker) executorFor(step config.Step) (Executor, error) {
	switch step.Type {
	case config.StepScript:
		return &ScriptExecutor{Prompter: w.Prompter}, nil
	case config.StepManual:
		return &ManualExecutor{Prompter: w.Prompter}, nil
	case config.StepInteractive, config.StepHeadless:
		return nil, fmt.Errorf("step %q: type %q not supported until a later milestone", step.Name, step.Type)
	default:
		return nil, fmt.Errorf("step %q: unknown type %q", step.Name, step.Type)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./runner/...`
Expected: PASS (all worker subtests).

- [ ] **Step 5: Commit**

```bash
git add runner/worker.go runner/worker_test.go
git commit -m "feat(runner): worker iteration loop with outcome resolution"
```

---

### Task 6: `cli` — `looper run` entrypoint

**Files:**
- Create: `main.go`
- Create: `cli/root.go`
- Create: `cli/run.go`
- Test: `cli/run_test.go`
- Create: `.looper/loops/example.yaml` (sample loop, committed)

**Interfaces:**
- Consumes: `config.LoadLoop` (Task 1); `runner.Worker`, `runner.StdinPrompter` (Tasks 3–5).
- Produces:
  - `func cli.Execute() error` — cobra root entrypoint called by `main`.
  - `func cli.RunLoop(opts RunOptions) error` — testable core: resolves the loop file, builds a `Worker`, runs it.
  - `cli.RunOptions` struct: `LoopName string`, `File string`, `BaseDir string`, `Workdir string`, `In io.Reader`, `Out io.Writer`.

- [ ] **Step 1: Add the cobra dependency**

Run:
```bash
go get github.com/spf13/cobra@latest
```
Expected: cobra added to `go.mod`.

- [ ] **Step 2: Write the failing test**

Create `cli/run_test.go`:
```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLoop_ExecutesScriptLoop(t *testing.T) {
	repo := t.TempDir()
	loopDir := filepath.Join(repo, ".looper", "loops")
	if err := os.MkdirAll(loopDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo, "ran.txt")
	loopYAML := "name: t\nmax_iterations: 1\nsteps:\n" +
		"  - name: do\n    type: script\n    run: \"echo hello > " + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(loopDir, "t.yaml"), []byte(loopYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RunLoop(RunOptions{
		LoopName: "t",
		BaseDir:  filepath.Join(repo, ".looper"),
		Workdir:  repo,
		In:       strings.NewReader(""),
		Out:      &strings.Builder{},
	})
	if err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("script step did not run (marker missing): %v", err)
	}
	// Run dir was created under BaseDir/runs/t.
	entries, _ := os.ReadDir(filepath.Join(repo, ".looper", "runs", "t"))
	if len(entries) != 1 {
		t.Errorf("expected 1 iteration run dir, got %d", len(entries))
	}
}

func TestRunLoop_MissingLoopErrors(t *testing.T) {
	err := RunLoop(RunOptions{
		LoopName: "nope",
		BaseDir:  filepath.Join(t.TempDir(), ".looper"),
		Out:      &strings.Builder{},
	})
	if err == nil {
		t.Fatal("expected error for missing loop file")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./cli/...`
Expected: FAIL — `undefined: RunLoop`, `undefined: RunOptions`.

- [ ] **Step 4: Write the run core**

Create `cli/run.go`:
```go
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jbofill/looper/config"
	"github.com/jbofill/looper/runner"
)

// RunOptions configures a single `looper run` invocation.
type RunOptions struct {
	LoopName string    // loads BaseDir/loops/<LoopName>.yaml when File is empty
	File     string    // explicit loop file path (overrides LoopName)
	BaseDir  string    // the .looper directory
	Workdir  string    // execution dir for workspace: shared
	In       io.Reader // prompter input (defaults to os.Stdin)
	Out      io.Writer // prompter/output (defaults to os.Stdout)
}

// RunLoop loads a loop and runs it single-worker, in-process.
func RunLoop(opts RunOptions) error {
	path := opts.File
	if path == "" {
		if opts.LoopName == "" {
			return fmt.Errorf("either a loop name or --file is required")
		}
		path = filepath.Join(opts.BaseDir, "loops", opts.LoopName+".yaml")
	}
	loop, err := config.LoadLoop(path)
	if err != nil {
		return err
	}

	in := opts.In
	if in == nil {
		in = os.Stdin
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	w := &runner.Worker{
		Loop:     loop,
		BaseDir:  opts.BaseDir,
		Workdir:  opts.Workdir,
		Prompter: &runner.StdinPrompter{In: in, Out: out},
	}
	fmt.Fprintf(out, "running loop %q\n", loop.Name)
	if err := w.Run(); err != nil {
		return err
	}
	fmt.Fprintf(out, "loop %q finished\n", loop.Name)
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./cli/...`
Expected: PASS.

- [ ] **Step 6: Write the cobra wiring**

Create `cli/root.go`:
```go
// Package cli wires looper's command-line interface.
package cli

import "github.com/spf13/cobra"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "looper",
		Short: "looper runs loop-based workflows",
	}
	root.AddCommand(newRunCmd())
	return root
}

// Execute runs the looper CLI.
func Execute() error {
	return newRootCmd().Execute()
}
```

Create `cli/run.go` addition — append the cobra command constructor to `cli/run.go`:
```go
// newRunCmd builds the `looper run` subcommand.
func newRunCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "run [loop-name]",
		Short: "Run a loop",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			opts := RunOptions{
				File:    file,
				BaseDir: filepath.Join(wd, ".looper"),
				Workdir: wd,
				In:      cmd.InOrStdin(),
				Out:     cmd.OutOrStdout(),
			}
			if len(args) == 1 {
				opts.LoopName = args[0]
			}
			return RunLoop(opts)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to a loop YAML file (overrides loop-name)")
	return cmd
}
```

Add the cobra import to `cli/run.go`'s import block: add `"github.com/spf13/cobra"`.

- [ ] **Step 7: Write main.go**

Create `main.go`:
```go
package main

import (
	"fmt"
	"os"

	"github.com/jbofill/looper/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 8: Create a sample loop and verify the full build + tests**

Create `.looper/loops/example.yaml`:
```yaml
name: example
max_iterations: 1
steps:
  - name: hello
    type: script
    run: "echo hello from looper"
  - name: confirm
    type: manual
```

Run:
```bash
go build ./...
go test ./...
```
Expected: build succeeds; all packages PASS.

- [ ] **Step 9: Manual end-to-end check**

Run:
```bash
go run . run example
```
Expected: prints `running loop "example"`, prints the hello step's execution, then prompts `Manual step "confirm". [a]dvance / [r]etry / [x]abort:`. Type `a` + Enter → prints `loop "example" finished`. Confirm a run dir exists: `ls .looper/runs/example/`.

- [ ] **Step 10: Commit**

```bash
git add main.go cli/ .looper/loops/example.yaml go.mod go.sum
git commit -m "feat(cli): looper run entrypoint"
```

---

## Self-Review

**Spec coverage (Milestone 1 scope only — build order steps 1–3 from §12):**
- Loop schema + validation → Task 1 ✓
- Run context (KV + artifacts + run dir + persistence + events + digest) → Task 2 ✓
- Script steps + outcome resolution + `signals_no_work` (exit 78) + output capture → Task 3 ✓
- Manual steps + human interaction → Task 4 ✓
- Worker iteration loop + termination (no-work, max_iterations) + abort/retry/advance → Task 5 ✓
- `WORKDIR` context var (shared workspace) → Task 5 ✓
- `interactive`/`headless` parse+validate but runner rejects → Tasks 1 & 5 ✓
- Runnable entrypoint → Task 6 ✓
- Deferred to later milestones (correctly absent here): daemon, gRPC, PTY, harness, hooks/sentinels, concurrency/fleet view, guided builder, resume, scheduling, global config.yaml/harness definitions. These are tracked in the spec §11/§12 and will each get their own plan.

**Placeholder scan:** No TBD/TODO; every code step contains complete code; every command has expected output. ✓

**Type consistency:** `Outcome` consts, `Executor.Run(rc, step)` signature, `Prompter` methods (`AskFailure`, `Manual`), `ScriptExecutor.Prompter`, `ManualExecutor.Prompter`, `Worker` fields, `runctx.RunContext`/`Event` API, and `config.Step`/`Loop` fields are used identically across Tasks 1–6. `NoWorkExitCode = 78` referenced consistently. ✓
