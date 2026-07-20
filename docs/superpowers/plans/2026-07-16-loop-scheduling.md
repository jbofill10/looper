# Loop Scheduling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a loop declare a repeating cron-style schedule (`every`/`at`/`cron`) in its YAML file, and have looperd fire a fresh one-off run of that loop (like the existing `RunLoopOnce`) each time the schedule ticks — surviving daemon restarts.

**Architecture:** `config.Schedule` normalizes the three YAML shorthand forms into one or more `robfig/cron/v3` spec strings. `daemon.Manager` owns a `*cron.Cron` scheduler plus a 30s rescan loop that treats each known project's `loops/*.yaml` files as the source of truth: it diffs each loop's normalized schedule against the currently-registered cron entries and adds/updates/removes them. A firing calls the same internal path `RunLoopOnce` uses, after checking the loop has no already-active run. New RPC `SetScheduleEnabled` and extended `ListLoops`/`LoopInfo` fields (`schedule_enabled`, `next_run`) let the TUI toggle and display it, mirroring the existing continuous-`enabled` plumbing.

**Tech Stack:** Go 1.26, `github.com/robfig/cron/v3` (new dependency), gRPC/protobuf (existing), Bubble Tea TUI (existing).

## Global Constraints

- Module path `github.com/jbofill10/looper`, Go 1.26 (from `go.mod`/CLAUDE.md).
- Every change goes through a branch + PR into `main`; never commit directly to `main` (CLAUDE.md workflow rule) — this plan's tasks should be committed on the feature branch already in use for this work.
- `go build ./...` and `go test ./...` must pass before opening the PR.
- Proto changes are hand-edited in `proto/looper.proto`, then regenerated into `rpc/*.pb.go` via `scripts/gen-proto.sh` (protoc + protoc-gen-go + protoc-gen-go-grpc must be on PATH — confirmed available at `/opt/homebrew/bin/protoc` and `$(go env GOPATH)/bin` in this environment). The generated files are committed.
- Exactly one of `Schedule.Every`, `Schedule.At`, `Schedule.Cron` may be set per loop; `Loop.Validate()` rejects zero or multiple set, or an unparseable value.
- Schedule ticks never stack: before firing, the daemon checks `Manager.activeRun(baseDir, loopName)` and skips the tick if a run is already active.
- All schedule times use the daemon process's local time zone; there is no per-loop timezone or catch-up/backfill for missed ticks (out of scope per the design spec).

---

### Task 1: `config.Schedule` type and cron-spec normalization

**Files:**
- Create: `config/schedule.go`
- Test: `config/schedule_test.go`
- Modify: `go.mod`, `go.sum` (add `github.com/robfig/cron/v3`)

**Interfaces:**
- Produces: `type Schedule struct { Every string; At []string; Cron string }` and `func (s *Schedule) CronSpecs() ([]string, error)` — used by Task 2 (`Loop.Validate`) and Task 5 (daemon sync logic).

- [ ] **Step 1: Add the `robfig/cron/v3` dependency**

Run: `go get github.com/robfig/cron/v3@latest`
Expected: `go.mod`/`go.sum` gain the dependency; run `go doc github.com/robfig/cron/v3 Cron` to confirm `New() *Cron`, `(*Cron) AddFunc(spec string, cmd func()) (EntryID, error)`, `(*Cron) Remove(id EntryID)`, `(*Cron) Entry(id EntryID) Entry`, `(*Cron) Start()` all exist as expected — if any signature differs, adjust the steps below accordingly before continuing.

- [ ] **Step 2: Write the failing tests for `CronSpecs`**

Create `config/schedule_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

func TestSchedule_CronSpecs_Every(t *testing.T) {
	s := &Schedule{Every: "15m"}
	specs, err := s.CronSpecs()
	if err != nil {
		t.Fatalf("CronSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0] != "@every 15m0s" {
		t.Errorf("specs = %v, want [\"@every 15m0s\"]", specs)
	}
}

func TestSchedule_CronSpecs_EveryCompoundDuration(t *testing.T) {
	s := &Schedule{Every: "1h30m"}
	specs, err := s.CronSpecs()
	if err != nil {
		t.Fatalf("CronSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0] != "@every 1h30m0s" {
		t.Errorf("specs = %v, want [\"@every 1h30m0s\"]", specs)
	}
}

func TestSchedule_CronSpecs_EveryInvalidDuration(t *testing.T) {
	s := &Schedule{Every: "not-a-duration"}
	if _, err := s.CronSpecs(); err == nil {
		t.Errorf("CronSpecs succeeded, want an error for an invalid duration")
	}
}

func TestSchedule_CronSpecs_EveryRejectsNonPositive(t *testing.T) {
	s := &Schedule{Every: "0s"}
	if _, err := s.CronSpecs(); err == nil {
		t.Errorf("CronSpecs succeeded, want an error for a non-positive duration")
	}
}

func TestSchedule_CronSpecs_AtSingleTime(t *testing.T) {
	s := &Schedule{At: []string{"09:00"}}
	specs, err := s.CronSpecs()
	if err != nil {
		t.Fatalf("CronSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0] != "0 9 * * *" {
		t.Errorf("specs = %v, want [\"0 9 * * *\"]", specs)
	}
}

func TestSchedule_CronSpecs_AtMultipleTimesProducesOneSpecEach(t *testing.T) {
	// A naive comma-joined single spec ("0,30 9,14 * * *") would wrongly
	// cross-product to 9:00, 9:30, 14:00, 14:30. Each `at` entry must
	// become its own independent spec.
	s := &Schedule{At: []string{"09:15", "14:30"}}
	specs, err := s.CronSpecs()
	if err != nil {
		t.Fatalf("CronSpecs: %v", err)
	}
	if len(specs) != 2 || specs[0] != "15 9 * * *" || specs[1] != "30 14 * * *" {
		t.Errorf("specs = %v, want [\"15 9 * * *\", \"30 14 * * *\"]", specs)
	}
}

func TestSchedule_CronSpecs_AtInvalidTime(t *testing.T) {
	for _, bad := range []string{"9am", "25:00", "09:60", "09"} {
		s := &Schedule{At: []string{bad}}
		if _, err := s.CronSpecs(); err == nil {
			t.Errorf("CronSpecs(%q) succeeded, want an error", bad)
		}
	}
}

func TestSchedule_CronSpecs_CronPassthrough(t *testing.T) {
	s := &Schedule{Cron: "0 9 * * 1-5"}
	specs, err := s.CronSpecs()
	if err != nil {
		t.Fatalf("CronSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0] != "0 9 * * 1-5" {
		t.Errorf("specs = %v, want [\"0 9 * * 1-5\"]", specs)
	}
}

func TestSchedule_CronSpecs_RejectsZeroSet(t *testing.T) {
	s := &Schedule{}
	if _, err := s.CronSpecs(); err == nil {
		t.Errorf("CronSpecs succeeded with no field set, want an error")
	}
}

func TestSchedule_CronSpecs_RejectsMultipleSet(t *testing.T) {
	s := &Schedule{Every: "15m", Cron: "0 9 * * *"}
	if _, err := s.CronSpecs(); err == nil {
		t.Errorf("CronSpecs succeeded with two fields set, want an error")
	}
	s2 := &Schedule{Every: "15m", At: []string{"09:00"}}
	if _, err := s2.CronSpecs(); err == nil {
		t.Errorf("CronSpecs succeeded with two fields set, want an error")
	}
}

func TestSchedule_CronSpecs_ErrorMentionsWhichFieldFailed(t *testing.T) {
	s := &Schedule{Every: "garbage"}
	_, err := s.CronSpecs()
	if err == nil || !strings.Contains(err.Error(), "every") {
		t.Errorf("err = %v, want it to mention \"every\"", err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./config/... -run TestSchedule_CronSpecs -v`
Expected: FAIL — `Schedule` and `CronSpecs` undefined.

- [ ] **Step 4: Implement `config/schedule.go`**

```go
// Package config: schedule.go defines a loop's optional repeating
// cron-style trigger and normalizes it into robfig/cron/v3 spec strings.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule declares a loop's repeating trigger. Exactly one of Every, At,
// or Cron must be set; CronSpecs enforces this.
type Schedule struct {
	Every string   `yaml:"every,omitempty"` // duration shorthand: "15m", "2h", "1h30m"
	At    []string `yaml:"at,omitempty"`    // daily times: ["09:00", "14:00", "20:00"]
	Cron  string   `yaml:"cron,omitempty"`  // raw 5-field cron expression
}

// CronSpecs normalizes s into one or more github.com/robfig/cron/v3 spec
// strings, each suitable for (*cron.Cron).AddFunc. Every and Cron always
// produce exactly one spec; At produces one spec per entry (never a single
// comma-joined spec, which would wrongly cross-product distinct
// hour/minute pairs — e.g. at: ["09:15","14:30"] must fire at 9:15 and
// 14:30, not also 9:30 and 14:15).
func (s *Schedule) CronSpecs() ([]string, error) {
	set := 0
	if s.Every != "" {
		set++
	}
	if len(s.At) > 0 {
		set++
	}
	if s.Cron != "" {
		set++
	}
	if set != 1 {
		return nil, fmt.Errorf("schedule must set exactly one of every, at, or cron (got %d set)", set)
	}

	switch {
	case s.Every != "":
		d, err := time.ParseDuration(s.Every)
		if err != nil {
			return nil, fmt.Errorf("invalid schedule.every %q: %w", s.Every, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("schedule.every must be positive, got %q", s.Every)
		}
		return []string{"@every " + d.String()}, nil

	case len(s.At) > 0:
		specs := make([]string, 0, len(s.At))
		for _, t := range s.At {
			hour, minute, err := parseClockTime(t)
			if err != nil {
				return nil, fmt.Errorf("invalid schedule.at %q: %w", t, err)
			}
			specs = append(specs, fmt.Sprintf("%d %d * * *", minute, hour))
		}
		return specs, nil

	default:
		return []string{s.Cron}, nil
	}
}

// parseClockTime parses "HH:MM" (24-hour, no seconds) into its hour and
// minute components.
func parseClockTime(t string) (hour, minute int, err error) {
	parts := strings.SplitN(t, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want HH:MM")
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour %q", parts[0])
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid minute %q", parts[1])
	}
	return hour, minute, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./config/... -run TestSchedule_CronSpecs -v`
Expected: PASS (all subtests).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum config/schedule.go config/schedule_test.go
git commit -m "feat: add Schedule config type with every/at/cron normalization"
```

---

### Task 2: Wire `Schedule` into `config.Loop` and its validation

**Files:**
- Modify: `config/loop.go`
- Test: `config/loop_test.go`

**Interfaces:**
- Consumes: `Schedule`, `(*Schedule) CronSpecs() ([]string, error)` from Task 1.
- Produces: `Loop.Schedule *Schedule` field, validated by `Loop.Validate()` — consumed by Task 5 (daemon sync logic reads `loop.Schedule`).

- [ ] **Step 1: Write the failing tests**

Add to `config/loop_test.go`:

```go
func TestLoop_ValidateAcceptsValidSchedule(t *testing.T) {
	l := &Loop{
		Name:     "a",
		Schedule: &Schedule{Every: "15m"},
		Steps:    []Step{{Name: "s", Type: StepScript, Run: "true"}},
	}
	if err := l.Validate(); err != nil {
		t.Errorf("Validate: %v, want nil", err)
	}
}

func TestLoop_ValidateRejectsInvalidSchedule(t *testing.T) {
	l := &Loop{
		Name:     "a",
		Schedule: &Schedule{Every: "15m", Cron: "0 9 * * *"}, // two fields set
		Steps:    []Step{{Name: "s", Type: StepScript, Run: "true"}},
	}
	if err := l.Validate(); err == nil {
		t.Errorf("Validate succeeded with an invalid schedule, want an error")
	}
}

func TestLoop_ValidateNilScheduleIsFine(t *testing.T) {
	l := &Loop{Name: "a", Steps: []Step{{Name: "s", Type: StepScript, Run: "true"}}}
	if err := l.Validate(); err != nil {
		t.Errorf("Validate: %v, want nil", err)
	}
}

func TestLoadLoop_WithScheduleField(t *testing.T) {
	p := writeTemp(t, `
name: nightly-report
schedule:
  at: ["21:00"]
steps:
  - name: s
    type: script
    run: "true"
`)
	loop, err := LoadLoop(p)
	if err != nil {
		t.Fatalf("LoadLoop: %v", err)
	}
	if loop.Schedule == nil || len(loop.Schedule.At) != 1 || loop.Schedule.At[0] != "21:00" {
		t.Errorf("loop.Schedule = %+v, want At: [\"21:00\"]", loop.Schedule)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./config/... -run 'TestLoop_Validate.*Schedule|TestLoadLoop_WithScheduleField' -v`
Expected: FAIL — `Loop` has no field `Schedule`.

- [ ] **Step 3: Add the field and validation hook**

In `config/loop.go`, add the field to `Loop` (after `TaskVar`, before `Steps`):

```go
// Loop is an ordered list of steps run as a repeating workflow.
type Loop struct {
	Name           string    `yaml:"name"`
	Concurrency    int       `yaml:"concurrency,omitempty"`
	MaxConcurrency int       `yaml:"max_concurrency,omitempty"`
	MaxIterations  int       `yaml:"max_iterations,omitempty"`
	Workspace      string    `yaml:"workspace,omitempty"` // shared|worktree
	TaskVar        string    `yaml:"task_var,omitempty"`  // the output var identifying a work unit; defaults to TASK_ID
	Schedule       *Schedule `yaml:"schedule,omitempty"`  // optional repeating trigger (see schedule.go)
	Steps          []Step    `yaml:"steps"`
}
```

In `Loop.Validate()`, add the check right after the `TaskVar` default block and before the `seen := map[string]bool{}` loop:

```go
	if l.Schedule != nil {
		if _, err := l.Schedule.CronSpecs(); err != nil {
			return fmt.Errorf("invalid schedule: %w", err)
		}
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./config/... -v`
Expected: PASS (all tests in the package, including the pre-existing ones — confirms nothing regressed).

- [ ] **Step 5: Commit**

```bash
git add config/loop.go config/loop_test.go
git commit -m "feat: validate a loop's optional schedule field"
```

---

### Task 3: Registry gains `KnownProjects` and per-loop `ScheduleEnabled`

**Files:**
- Modify: `daemon/registry.go`
- Test: `daemon/registry_test.go`

**Interfaces:**
- Produces: `registryEntry.ScheduleEnabled *bool` + `(registryEntry) scheduleEnabled() bool`; `registryFile.KnownProjects []string`; `loadRegistryFile(path) (registryFile, error)`, `saveRegistryFile(path, registryFile) error`; `recordKnownProject(path, baseDir string) error`; `loadKnownProjects(path string) ([]string, error)`. `loadRegistry`/`saveRegistry`'s existing signatures (`map[string]registryEntry`) are preserved for Tasks 4/6's existing callers, now implemented in terms of the new file-level functions so they no longer clobber `KnownProjects` on a plain `saveRegistry` call.

- [ ] **Step 1: Write the failing tests**

Add to `daemon/registry_test.go`:

```go
func TestRegistryEntry_ScheduleEnabledDefaultsTrueWhenUnset(t *testing.T) {
	e := registryEntry{}
	if !e.scheduleEnabled() {
		t.Errorf("scheduleEnabled() = false for a zero-value entry, want true")
	}
}

func TestRegistryEntry_ScheduleEnabledRespectsExplicitFalse(t *testing.T) {
	f := false
	e := registryEntry{ScheduleEnabled: &f}
	if e.scheduleEnabled() {
		t.Errorf("scheduleEnabled() = true, want false")
	}
}

func TestSaveRegistry_PreservesKnownProjectsAcrossSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := recordKnownProject(path, "/proj1/.looper"); err != nil {
		t.Fatalf("recordKnownProject: %v", err)
	}

	// A plain saveRegistry (as SetLoopEnabled/RenameLoop/DeleteLoop call it)
	// must not wipe out KnownProjects recorded via recordKnownProject.
	entries := map[string]registryEntry{
		registryKey("/proj1/.looper", "a"): {BaseDir: "/proj1/.looper", LoopName: "a", Enabled: true},
	}
	if err := saveRegistry(path, entries); err != nil {
		t.Fatalf("saveRegistry: %v", err)
	}

	projects, err := loadKnownProjects(path)
	if err != nil {
		t.Fatalf("loadKnownProjects: %v", err)
	}
	if len(projects) != 1 || projects[0] != "/proj1/.looper" {
		t.Errorf("projects = %v, want [\"/proj1/.looper\"]", projects)
	}
}

func TestRecordKnownProject_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	for i := 0; i < 3; i++ {
		if err := recordKnownProject(path, "/proj1/.looper"); err != nil {
			t.Fatalf("recordKnownProject: %v", err)
		}
	}
	projects, err := loadKnownProjects(path)
	if err != nil {
		t.Fatalf("loadKnownProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("projects = %v, want exactly 1 entry after 3 identical calls", projects)
	}
}

func TestLoadKnownProjects_MissingFileReturnsEmpty(t *testing.T) {
	projects, err := loadKnownProjects(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("loadKnownProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("projects = %v, want empty", projects)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./daemon/... -run 'TestRegistryEntry_ScheduleEnabled|TestSaveRegistry_PreservesKnownProjects|TestRecordKnownProject_IsIdempotent|TestLoadKnownProjects_MissingFileReturnsEmpty' -v`
Expected: FAIL — `registryEntry.ScheduleEnabled`, `recordKnownProject`, `loadKnownProjects` undefined.

- [ ] **Step 3: Implement the changes in `daemon/registry.go`**

Replace the whole file's content with:

```go
// Package daemon: registry.go implements the daemon-wide enabled-loops
// registry. looperd is a single per-user process shared across however
// many project directories invoke it (see client.SocketPath's per-uid
// path), so a per-project state file would be invisible to the daemon on
// its own restart. Instead, one registry file — resolved the same way as
// the socket path — tracks every (base_dir, loop_name) pair's enabled
// flag, plus every base_dir ever seen (KnownProjects, used to rediscover
// schedules on daemon restart), so AutoResume and the schedule rescan can
// restart/re-register without discovering project directories.
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
	// ScheduleEnabled is a *bool (not bool) so that an entry created only
	// for the continuous Enabled flag above — which never touched
	// scheduling — doesn't read as "schedule explicitly disabled". nil
	// means "not yet explicitly toggled"; scheduleEnabled() below treats
	// that as enabled, matching how a loop's schedule is enabled by
	// default the first time it's discovered.
	ScheduleEnabled *bool `json:"scheduleEnabled,omitempty"`
}

// scheduleEnabled reports whether e's schedule (if its loop has one) is
// currently enabled. Absence of an explicit toggle (nil) means enabled.
func (e registryEntry) scheduleEnabled() bool {
	return e.ScheduleEnabled == nil || *e.ScheduleEnabled
}

// registryFile is registry.json's on-disk shape.
type registryFile struct {
	Loops map[string]registryEntry `json:"loops"`
	// KnownProjects is every base_dir ever passed to Manager.ListLoops,
	// recorded so a restarted daemon knows which project directories to
	// rescan for schedules without waiting for a client to call ListLoops
	// again first.
	KnownProjects []string `json:"knownProjects,omitempty"`
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

// loadRegistryFile reads and parses the whole registry file at path,
// returning a zero-value (empty Loops map, nil KnownProjects) registryFile
// — not an error — if the file doesn't exist yet.
func loadRegistryFile(path string) (registryFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return registryFile{Loops: map[string]registryEntry{}}, nil
		}
		return registryFile{}, fmt.Errorf("read registry %q: %w", path, err)
	}
	var rf registryFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return registryFile{}, fmt.Errorf("parse registry %q: %w", path, err)
	}
	if rf.Loops == nil {
		rf.Loops = map[string]registryEntry{}
	}
	return rf, nil
}

// saveRegistryFile writes rf to path as JSON, creating any missing parent
// directories.
func saveRegistryFile(path string, rf registryFile) error {
	data, err := json.MarshalIndent(rf, "", "  ")
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

// loadRegistry reads the registry file at path and returns just its Loops
// map, for the many callers that only care about per-loop entries.
func loadRegistry(path string) (map[string]registryEntry, error) {
	rf, err := loadRegistryFile(path)
	if err != nil {
		return nil, err
	}
	return rf.Loops, nil
}

// saveRegistry writes entries as the registry's Loops map, preserving
// whatever KnownProjects the file already has (it reads the file first,
// so this never clobbers KnownProjects the way a naive
// registryFile{Loops: entries} overwrite would).
func saveRegistry(path string, entries map[string]registryEntry) error {
	rf, err := loadRegistryFile(path)
	if err != nil {
		return err
	}
	rf.Loops = entries
	return saveRegistryFile(path, rf)
}

// loadKnownProjects returns every base_dir recorded via recordKnownProject,
// or an empty slice (not an error) if the registry file doesn't exist yet.
func loadKnownProjects(path string) ([]string, error) {
	rf, err := loadRegistryFile(path)
	if err != nil {
		return nil, err
	}
	return rf.KnownProjects, nil
}

// recordKnownProject appends baseDir to the registry's KnownProjects list
// if it isn't already present. It is idempotent and a no-op write (no
// disk write at all) when baseDir is already known.
func recordKnownProject(path, baseDir string) error {
	rf, err := loadRegistryFile(path)
	if err != nil {
		return err
	}
	for _, p := range rf.KnownProjects {
		if p == baseDir {
			return nil
		}
	}
	rf.KnownProjects = append(rf.KnownProjects, baseDir)
	return saveRegistryFile(path, rf)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./daemon/... -run 'TestRegistry|TestRecordKnownProject|TestLoadKnownProjects|TestSaveRegistry' -v`
Expected: PASS (all subtests, including the pre-existing `TestRegistry_*` tests — confirms `loadRegistry`/`saveRegistry`'s external behavior is unchanged for existing callers).

- [ ] **Step 5: Commit**

```bash
git add daemon/registry.go daemon/registry_test.go
git commit -m "feat: registry gains KnownProjects and per-loop ScheduleEnabled"
```

---

### Task 4: `Manager` gains a cron scheduler and `SetScheduleEnabled`

**Files:**
- Modify: `daemon/manager.go`
- Test: `daemon/manager_test.go`

**Interfaces:**
- Consumes: `registryEntry.scheduleEnabled()`, `registryFile`/`loadRegistryFile`/`saveRegistryFile` from Task 3.
- Produces: `Manager.scheduler *cron.Cron`, `Manager.scheduleEntries map[string]scheduledEntry` (unexported, used by Task 5), `func (m *Manager) SetScheduleEnabled(loopName, baseDir, workdir string, enabled bool) error`, `func workdirFromBaseDir(baseDir string) string`.

- [ ] **Step 1: Write the failing test**

Add to `daemon/manager_test.go`:

```go
func TestManager_SetScheduleEnabledPersistsWithoutStartingARun(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:     "a",
		Schedule: &config.Schedule{Every: "1h"},
		Steps:    []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	if err := m.SetScheduleEnabled("a", baseDir, dir, false); err != nil {
		t.Fatalf("SetScheduleEnabled: %v", err)
	}

	if runID := m.activeRun(baseDir, "a"); runID != "" {
		t.Errorf("SetScheduleEnabled started a run (%s), want none", runID)
	}

	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	entry := registry[registryKey(baseDir, "a")]
	if entry.scheduleEnabled() {
		t.Errorf("registry entry still reports schedule enabled after disabling")
	}
}

func TestWorkdirFromBaseDir(t *testing.T) {
	got := workdirFromBaseDir("/Users/juan/proj1/.looper")
	if got != "/Users/juan/proj1" {
		t.Errorf("workdirFromBaseDir = %q, want %q", got, "/Users/juan/proj1")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./daemon/... -run 'TestManager_SetScheduleEnabledPersists|TestWorkdirFromBaseDir' -v`
Expected: FAIL — `Manager.SetScheduleEnabled` and `workdirFromBaseDir` undefined.

- [ ] **Step 3: Add the scheduler fields and `SetScheduleEnabled`**

In `daemon/manager.go`, add the import (alongside the existing `"github.com/jbofill10/looper/runner"` import block):

```go
	"github.com/robfig/cron/v3"
```

The `Manager` struct's final field today is:

```go
	registryMu sync.Mutex
}
```

Replace just that closing fragment (everything from `registryMu sync.Mutex` through the struct's closing `}`) with:

```go
	registryMu sync.Mutex

	// scheduler runs every registered schedule tick for every known
	// project. scheduleMu guards scheduleEntries (a Manager-lifetime map,
	// not reloaded from disk); the registry file remains the source of
	// truth for which loops *should* have entries — see schedule.go.
	scheduler       *cron.Cron
	scheduleMu      sync.Mutex
	scheduleEntries map[string]scheduledEntry
}

// scheduledEntry tracks one loop's currently-registered cron job(s) (one
// job per config.Schedule.At entry, or a single job for every/cron), so a
// rescan can detect "spec changed" and swap the registration, or "loop
// removed/disabled" and tear it down.
type scheduledEntry struct {
	ids  []cron.EntryID
	spec string // all specs joined with "|", used to detect a changed schedule
}
```

Every field and comment already in the struct above `registryMu` (global, looperBin, newID, newReqID, mu, runs, subs, nextSubID, registryPath, and the comment on `registryMu` itself) stays exactly as-is — only the closing `}` and everything after `registryMu sync.Mutex` changes.

Update `NewManager` to construct and start the scheduler:

```go
// NewManager returns a Manager for orchestrating loops. A nil global uses
// config.DefaultGlobal(). The returned Manager immediately starts its
// internal cron scheduler and a background schedule-rescan loop (see
// schedule.go); both run for the Manager's lifetime, which in practice is
// the daemon process's lifetime.
func NewManager(global *config.Global, looperBin string) *Manager {
	if global == nil {
		global = config.DefaultGlobal()
	}
	m := &Manager{
		global:          global,
		looperBin:       looperBin,
		newID:           newCounter("run"),
		newReqID:        newCounter("req"),
		runs:            map[string]*runEntry{},
		subs:            map[int]*subscriber{},
		registryPath:    defaultRegistryPath(),
		scheduler:       cron.New(),
		scheduleEntries: map[string]scheduledEntry{},
	}
	m.scheduler.Start()
	go m.scheduleRescanLoop()
	return m
}
```

Add `SetScheduleEnabled` and `workdirFromBaseDir` near `SetLoopEnabled`'s definition would live (in `catalog.go`) — but since `SetLoopEnabled` is actually defined in `manager.go` per the file read earlier... (it is: `Manager.StartLoop`, `StopLoop` etc. live in manager.go; `SetLoopEnabled` lives in catalog.go). Add `SetScheduleEnabled` at the end of `daemon/manager.go` instead, since it's scheduler-specific plumbing:

```go
// SetScheduleEnabled persists loopName's schedule-enabled flag (keyed by
// base_dir), independent of its continuous Enabled flag. Unlike
// SetLoopEnabled, it never starts or stops a run itself — it only affects
// whether future schedule ticks fire; the next rescan (at most
// scheduleRescanInterval later) picks up the change.
func (m *Manager) SetScheduleEnabled(loopName, baseDir, workdir string, enabled bool) error {
	m.registryMu.Lock()
	defer m.registryMu.Unlock()
	rf, err := loadRegistryFile(m.registryPath)
	if err != nil {
		return err
	}
	key := registryKey(baseDir, loopName)
	entry := rf.Loops[key]
	entry.BaseDir = baseDir
	entry.Workdir = workdir
	entry.LoopName = loopName
	e := enabled
	entry.ScheduleEnabled = &e
	rf.Loops[key] = entry
	return saveRegistryFile(m.registryPath, rf)
}

// workdirFromBaseDir derives a project's working directory from its
// base_dir, following the convention used everywhere else in looper
// (cli/loops.go, tui/program.go): base_dir is always "<workdir>/.looper".
// It's needed when firing a schedule for a project that was only ever seen
// via ListLoops (recorded in KnownProjects), which carries no workdir of
// its own.
func workdirFromBaseDir(baseDir string) string {
	return filepath.Dir(baseDir)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./daemon/... -run 'TestManager_SetScheduleEnabledPersists|TestWorkdirFromBaseDir' -v`
Expected: PASS.

- [ ] **Step 5: Run the full daemon package test suite to confirm no regressions**

Run: `go test ./daemon/... -v`
Expected: PASS (all tests, including every pre-existing `TestManager_*`/`TestRegistry_*` test).

- [ ] **Step 6: Commit**

```bash
git add daemon/manager.go daemon/manager_test.go go.mod go.sum
git commit -m "feat: Manager gains a cron scheduler and SetScheduleEnabled"
```

---

### Task 5: Schedule sync/rescan/fire logic

**Files:**
- Create: `daemon/schedule.go`
- Test: `daemon/schedule_test.go`

**Interfaces:**
- Consumes: `Manager.scheduler`, `Manager.scheduleEntries`, `Manager.scheduleMu`, `Manager.registryMu`, `Manager.registryPath`, `Manager.activeRun`, `Manager.RunLoopOnce`, `workdirFromBaseDir` (Task 4); `registryEntry.scheduleEnabled()`, `loadRegistryFile` (Task 3); `config.Loop.Schedule`, `(*config.Schedule) CronSpecs()` (Tasks 1–2); `config.LoadLoopLenient`, `isYAMLFile` (existing, from `daemon/catalog.go`).
- Produces: `func (m *Manager) rescanSchedules()`, `func (m *Manager) syncProjectSchedules(baseDir string, registry map[string]registryEntry)`, `func (m *Manager) nextRunFor(key string) time.Time` — the latter consumed by Task 6 (`ListLoops`).

- [ ] **Step 1: Write the failing tests**

Create `daemon/schedule_test.go`:

```go
package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jbofill10/looper/config"
)

func TestManager_SyncProjectSchedulesRegistersAnEveryEntry(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:     "a",
		Schedule: &config.Schedule{Every: "1h"},
		Steps:    []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	m.syncProjectSchedules(baseDir, registry)

	key := registryKey(baseDir, "a")
	m.scheduleMu.Lock()
	entry, ok := m.scheduleEntries[key]
	m.scheduleMu.Unlock()
	if !ok || len(entry.ids) != 1 {
		t.Fatalf("scheduleEntries[%q] = %+v, ok=%v, want one registered entry", key, entry, ok)
	}

	next := m.nextRunFor(key)
	if next.IsZero() || next.Before(time.Now()) {
		t.Errorf("nextRunFor = %v, want a future time", next)
	}
}

func TestManager_SyncProjectSchedulesRegistersOneEntryPerAtTime(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:     "a",
		Schedule: &config.Schedule{At: []string{"09:00", "14:00", "20:00"}},
		Steps:    []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	registry, _ := loadRegistry(m.registryPath)
	m.syncProjectSchedules(baseDir, registry)

	key := registryKey(baseDir, "a")
	m.scheduleMu.Lock()
	entry := m.scheduleEntries[key]
	m.scheduleMu.Unlock()
	if len(entry.ids) != 3 {
		t.Errorf("got %d registered cron entries, want 3 (one per `at` time)", len(entry.ids))
	}
}

func TestManager_SyncProjectSchedulesUpdatesOnSpecChange(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:     "a",
		Schedule: &config.Schedule{Every: "1h"},
		Steps:    []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	registry, _ := loadRegistry(m.registryPath)
	m.syncProjectSchedules(baseDir, registry)

	key := registryKey(baseDir, "a")
	m.scheduleMu.Lock()
	firstIDs := append([]cronEntryIDSlice{}, entryIDs(m.scheduleEntries[key])...)
	m.scheduleMu.Unlock()

	// Edit the loop file's schedule and rescan.
	loop.Schedule = &config.Schedule{Every: "2h"}
	writeLoopFile(t, filepath.Join(baseDir, "loops"), loop)
	registry, _ = loadRegistry(m.registryPath)
	m.syncProjectSchedules(baseDir, registry)

	m.scheduleMu.Lock()
	secondIDs := append([]cronEntryIDSlice{}, entryIDs(m.scheduleEntries[key])...)
	m.scheduleMu.Unlock()

	if len(secondIDs) != 1 || secondIDs[0] == firstIDs[0] {
		t.Errorf("expected the entry to be re-registered under a new cron.EntryID after a spec change; first=%v second=%v", firstIDs, secondIDs)
	}
}

func TestManager_SyncProjectSchedulesRemovesEntryWhenScheduleDeleted(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:     "a",
		Schedule: &config.Schedule{Every: "1h"},
		Steps:    []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	registry, _ := loadRegistry(m.registryPath)
	m.syncProjectSchedules(baseDir, registry)

	loop.Schedule = nil
	writeLoopFile(t, filepath.Join(baseDir, "loops"), loop)
	registry, _ = loadRegistry(m.registryPath)
	m.syncProjectSchedules(baseDir, registry)

	key := registryKey(baseDir, "a")
	m.scheduleMu.Lock()
	_, ok := m.scheduleEntries[key]
	m.scheduleMu.Unlock()
	if ok {
		t.Errorf("scheduleEntries[%q] still present after the loop's schedule was removed", key)
	}
}

func TestManager_SyncProjectSchedulesRemovesEntryWhenScheduleDisabled(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:     "a",
		Schedule: &config.Schedule{Every: "1h"},
		Steps:    []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	if err := m.SetScheduleEnabled("a", baseDir, dir, false); err != nil {
		t.Fatalf("SetScheduleEnabled: %v", err)
	}
	registry, _ := loadRegistry(m.registryPath)
	m.syncProjectSchedules(baseDir, registry)

	key := registryKey(baseDir, "a")
	m.scheduleMu.Lock()
	_, ok := m.scheduleEntries[key]
	m.scheduleMu.Unlock()
	if ok {
		t.Errorf("scheduleEntries[%q] present for a schedule-disabled loop", key)
	}
}

func TestManager_FireScheduleSkipsWhenAlreadyActive(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:     "a",
		Schedule: &config.Schedule{Every: "1h"},
		Steps:    []config.Step{{Name: "s", Type: config.StepInteractive, Prompt: "p"}},
	}
	baseDir := writeLoopsDir(t, dir, loop)

	m := newTestManager(t)
	firstRunID, err := m.SetLoopEnabled("a", baseDir, dir, true)
	if err != nil {
		t.Fatalf("SetLoopEnabled: %v", err)
	}
	// The interactive step never advances in this test, so the run blocks
	// until stopped; stop it before t.TempDir() cleanup races it.
	defer m.StopLoop(firstRunID)

	m.fireSchedule(baseDir, "a")

	// fireSchedule must not have started a second run: activeRun still
	// reports the original one.
	if got := m.activeRun(baseDir, "a"); got != firstRunID {
		t.Errorf("activeRun = %q after fireSchedule, want unchanged %q (a stacked run means the skip check didn't fire)", got, firstRunID)
	}
}

func TestManager_RescanSchedulesCoversEveryKnownProject(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	loop1 := &config.Loop{Name: "a", Schedule: &config.Schedule{Every: "1h"}, Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}}}
	loop2 := &config.Loop{Name: "b", Schedule: &config.Schedule{Every: "1h"}, Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}}}
	baseDir1 := writeLoopsDir(t, dir1, loop1)
	baseDir2 := writeLoopsDir(t, dir2, loop2)

	m := newTestManager(t)
	if _, err := m.ListLoops(baseDir1); err != nil {
		t.Fatalf("ListLoops(1): %v", err)
	}
	if _, err := m.ListLoops(baseDir2); err != nil {
		t.Fatalf("ListLoops(2): %v", err)
	}

	m.rescanSchedules()

	for _, key := range []string{registryKey(baseDir1, "a"), registryKey(baseDir2, "b")} {
		m.scheduleMu.Lock()
		_, ok := m.scheduleEntries[key]
		m.scheduleMu.Unlock()
		if !ok {
			t.Errorf("scheduleEntries[%q] missing after rescanSchedules", key)
		}
	}
}
```

Note: the update-on-spec-change test above references a small helper `entryIDs` and a `cronEntryIDSlice` type alias purely to keep the test readable; add these to the top of `daemon/schedule_test.go` instead of inlining `cron.EntryID` comparisons:

```go
type cronEntryIDSlice = cron.EntryID

func entryIDs(e scheduledEntry) []cron.EntryID { return e.ids }
```

Add `"github.com/robfig/cron/v3"` to this test file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./daemon/... -run 'TestManager_SyncProjectSchedules|TestManager_FireSchedule|TestManager_RescanSchedules' -v`
Expected: FAIL — `syncProjectSchedules`, `fireSchedule`, `rescanSchedules`, `nextRunFor` undefined.

- [ ] **Step 3: Implement `daemon/schedule.go`**

```go
// Package daemon: schedule.go implements looperd's cron-style loop
// scheduling. A background rescan loop treats each known project's
// loops/*.yaml files as the source of truth for schedules (consistent
// with how ListLoops already scans the filesystem), diffing them against
// currently-registered cron entries and adding/updating/removing as
// needed. A firing triggers the same one-off run RunLoopOnce uses, unless
// the loop already has an active run.
package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jbofill10/looper/config"
	"github.com/robfig/cron/v3"
)

// scheduleRescanInterval is how often the background loop re-scans every
// known project's loops/*.yaml files for schedule changes.
const scheduleRescanInterval = 30 * time.Second

// scheduleRescanLoop runs rescanSchedules once immediately, then again
// every scheduleRescanInterval, for the Manager's lifetime (the daemon
// process's lifetime — there is no shutdown hook for this loop, matching
// the rest of Manager's per-run goroutines, which likewise exit only when
// their run ends or the process does).
func (m *Manager) scheduleRescanLoop() {
	m.rescanSchedules()
	ticker := time.NewTicker(scheduleRescanInterval)
	defer ticker.Stop()
	for range ticker.C {
		m.rescanSchedules()
	}
}

// rescanSchedules re-syncs every known project's schedules against the
// current registry and loop files.
func (m *Manager) rescanSchedules() {
	m.registryMu.Lock()
	rf, err := loadRegistryFile(m.registryPath)
	m.registryMu.Unlock()
	if err != nil {
		return
	}
	for _, baseDir := range rf.KnownProjects {
		m.syncProjectSchedules(baseDir, rf.Loops)
	}
}

// syncProjectSchedules loads every loops/*.yaml file under baseDir and
// reconciles this Manager's registered cron entries against them:
//   - a loop with a Schedule, schedule-enabled, not yet registered (or
//     registered under a stale spec) gets its cron entry (re)added
//   - a loop with no Schedule, a schedule-disabled loop, or a since-removed
//     loop file has its cron entry (if any) removed
//
// Unreadable/unparseable loop files are skipped, consistent with
// ListLoops's existing lenient scan.
func (m *Manager) syncProjectSchedules(baseDir string, registry map[string]registryEntry) {
	loopsDir := filepath.Join(baseDir, "loops")
	entries, err := os.ReadDir(loopsDir)
	if err != nil {
		return
	}

	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !isYAMLFile(e.Name()) {
			continue
		}
		loop, err := config.LoadLoopLenient(filepath.Join(loopsDir, e.Name()))
		if err != nil || loop.Schedule == nil {
			continue
		}

		key := registryKey(baseDir, loop.Name)
		seen[key] = true

		if !registry[key].scheduleEnabled() {
			m.removeScheduleEntry(key)
			continue
		}

		specs, err := loop.Schedule.CronSpecs()
		if err != nil {
			continue
		}
		m.upsertScheduleEntry(key, specs, baseDir, loop.Name)
	}

	m.scheduleMu.Lock()
	var stale []string
	for key := range m.scheduleEntries {
		if strings.HasPrefix(key, baseDir+"|") && !seen[key] {
			stale = append(stale, key)
		}
	}
	m.scheduleMu.Unlock()
	for _, key := range stale {
		m.removeScheduleEntry(key)
	}
}

// upsertScheduleEntry registers specs for key if it isn't already
// registered under the same joined spec; otherwise it removes the stale
// registration first. baseDir/loopName are captured by the cron callback
// so firing doesn't need to look them back up from key.
func (m *Manager) upsertScheduleEntry(key string, specs []string, baseDir, loopName string) {
	joined := strings.Join(specs, "|")

	m.scheduleMu.Lock()
	if existing, ok := m.scheduleEntries[key]; ok {
		if existing.spec == joined {
			m.scheduleMu.Unlock()
			return
		}
		for _, id := range existing.ids {
			m.scheduler.Remove(id)
		}
	}
	m.scheduleMu.Unlock()

	ids := make([]cron.EntryID, 0, len(specs))
	for _, spec := range specs {
		id, err := m.scheduler.AddFunc(spec, func() { m.fireSchedule(baseDir, loopName) })
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}

	m.scheduleMu.Lock()
	m.scheduleEntries[key] = scheduledEntry{ids: ids, spec: joined}
	m.scheduleMu.Unlock()
}

// removeScheduleEntry unregisters key's cron entries, if any.
func (m *Manager) removeScheduleEntry(key string) {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	existing, ok := m.scheduleEntries[key]
	if !ok {
		return
	}
	for _, id := range existing.ids {
		m.scheduler.Remove(id)
	}
	delete(m.scheduleEntries, key)
}

// fireSchedule is the callback registered for every cron entry: unless
// loopName already has an active run in baseDir (a prior tick still in
// flight, or the loop separately running continuously via SetLoopEnabled),
// it starts a fresh one-off run via RunLoopOnce. workdirFromBaseDir
// recovers the project's working directory from baseDir, since a
// schedule-only project (never continuously enabled) has no registry
// entry to read a workdir from.
func (m *Manager) fireSchedule(baseDir, loopName string) {
	if m.activeRun(baseDir, loopName) != "" {
		return
	}
	m.RunLoopOnce(loopName, "", baseDir, workdirFromBaseDir(baseDir))
}

// nextRunFor returns the soonest upcoming firing time across all of key's
// registered cron entries, or the zero Time if key has none registered.
func (m *Manager) nextRunFor(key string) time.Time {
	m.scheduleMu.Lock()
	entry, ok := m.scheduleEntries[key]
	m.scheduleMu.Unlock()
	if !ok {
		return time.Time{}
	}

	var next time.Time
	for _, id := range entry.ids {
		n := m.scheduler.Entry(id).Next
		if n.IsZero() {
			continue
		}
		if next.IsZero() || n.Before(next) {
			next = n
		}
	}
	return next
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./daemon/... -run 'TestManager_SyncProjectSchedules|TestManager_FireSchedule|TestManager_RescanSchedules' -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./daemon/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add daemon/schedule.go daemon/schedule_test.go
git commit -m "feat: schedule sync/rescan/fire logic"
```

---

### Task 6: `ListLoops` records known projects and reports schedule state

**Files:**
- Modify: `daemon/catalog.go`
- Test: `daemon/catalog_test.go`

**Interfaces:**
- Consumes: `recordKnownProject` (Task 3), `registryEntry.scheduleEnabled()` (Task 3), `Manager.nextRunFor` (Task 5).
- Produces: `LoopSummary.ScheduleEnabled bool`, `LoopSummary.NextRun time.Time` — consumed by Task 7 (`service.go`'s RPC mapping).

- [ ] **Step 1: Write the failing tests**

Add to `daemon/catalog_test.go`:

```go
func TestManager_ListLoopsReportsScheduleState(t *testing.T) {
	dir := t.TempDir()
	baseDir := writeLoopsDir(t, dir,
		&config.Loop{Name: "scheduled", Schedule: &config.Schedule{Every: "1h"}, Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}}},
		&config.Loop{Name: "unscheduled", Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}}},
	)

	m := newTestManager(t)
	// The first ListLoops call records baseDir as known but hasn't
	// rescanned yet; call the rescan directly (bypassing the 30s ticker)
	// so the second ListLoops call observes registered entries.
	if _, err := m.ListLoops(baseDir); err != nil {
		t.Fatalf("ListLoops (prime): %v", err)
	}
	m.rescanSchedules()

	summaries, err := m.ListLoops(baseDir)
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	// sorted by name: scheduled, unscheduled
	if !summaries[0].ScheduleEnabled || summaries[0].NextRun.IsZero() {
		t.Errorf("loop \"scheduled\" = %+v, want ScheduleEnabled=true with a non-zero NextRun", summaries[0])
	}
	if summaries[1].ScheduleEnabled || !summaries[1].NextRun.IsZero() {
		t.Errorf("loop \"unscheduled\" = %+v, want ScheduleEnabled=false with a zero NextRun", summaries[1])
	}
}

func TestManager_ListLoopsRecordsKnownProject(t *testing.T) {
	dir := t.TempDir()
	baseDir := writeLoopsDir(t, dir, &config.Loop{Name: "a", Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}}})

	m := newTestManager(t)
	if _, err := m.ListLoops(baseDir); err != nil {
		t.Fatalf("ListLoops: %v", err)
	}

	projects, err := loadKnownProjects(m.registryPath)
	if err != nil {
		t.Fatalf("loadKnownProjects: %v", err)
	}
	if len(projects) != 1 || projects[0] != baseDir {
		t.Errorf("projects = %v, want [%q]", projects, baseDir)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./daemon/... -run 'TestManager_ListLoopsReportsScheduleState|TestManager_ListLoopsRecordsKnownProject' -v`
Expected: FAIL — `LoopSummary` has no `ScheduleEnabled`/`NextRun` fields, and `ListLoops` doesn't yet record the project.

- [ ] **Step 3: Update `daemon/catalog.go`**

Add `"time"` to the import block. Update `LoopSummary`:

```go
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
	// ScheduleEnabled is true only when the loop has a Schedule AND its
	// schedule toggle (see Manager.SetScheduleEnabled) is enabled.
	ScheduleEnabled bool
	// NextRun is the soonest upcoming scheduled firing, or the zero Time
	// if the loop has no schedule or ScheduleEnabled is false.
	NextRun time.Time
}
```

Update `ListLoops` to record the project and populate the new fields — replace the function body:

```go
// ListLoops scans <baseDir>/loops/*.yaml and cross-references the
// registry (enabled flag, schedule state) and active runs (RunID), sorted
// by loop name. A missing loops directory returns an empty slice, not an
// error. It also records baseDir in the registry's KnownProjects (see
// recordKnownProject) so the background schedule rescan discovers this
// project even if no loop in it is ever continuously enabled.
func (m *Manager) ListLoops(baseDir string) ([]LoopSummary, error) {
	loopsDir := filepath.Join(baseDir, "loops")
	entries, err := os.ReadDir(loopsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading loops directory: %w", err)
	}

	m.registryMu.Lock()
	err = recordKnownProject(m.registryPath, baseDir)
	m.registryMu.Unlock()
	if err != nil {
		return nil, err
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

		key := registryKey(baseDir, loop.Name)
		scheduleEnabled := loop.Schedule != nil && registry[key].scheduleEnabled()
		var nextRun time.Time
		if scheduleEnabled {
			nextRun = m.nextRunFor(key)
		}

		out = append(out, LoopSummary{
			Name:            loop.Name,
			Path:            path,
			Enabled:         registry[key].Enabled,
			Steps:           stepNames,
			RunID:           activeByLoop[loop.Name],
			ScheduleEnabled: scheduleEnabled,
			NextRun:         nextRun,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./daemon/... -run 'TestManager_ListLoopsReportsScheduleState|TestManager_ListLoopsRecordsKnownProject' -v`
Expected: PASS.

- [ ] **Step 5: Run the full daemon package test suite**

Run: `go test ./daemon/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add daemon/catalog.go daemon/catalog_test.go
git commit -m "feat: ListLoops records known projects and reports schedule state"
```

---

### Task 7: Proto + `SetScheduleEnabled` RPC + `ListLoops` wire mapping

**Files:**
- Modify: `proto/looper.proto`
- Modify (generated, do not hand-edit beyond running the script): `rpc/looper.pb.go`, `rpc/looper_grpc.pb.go`
- Modify: `daemon/service.go`
- Test: `daemon/service_test.go`

**Interfaces:**
- Consumes: `Manager.SetScheduleEnabled` (Task 4), `LoopSummary.ScheduleEnabled`/`NextRun` (Task 6).
- Produces: `rpc.SetScheduleEnabledRequest`/`Response`, `rpc.LooperClient.SetScheduleEnabled`, `rpc.LoopInfo.ScheduleEnabled`/`NextRun` — consumed by Task 8/9 (TUI).

- [ ] **Step 1: Edit `proto/looper.proto`**

Add the new RPC to the `Looper` service, right after `DeleteLoop`:

```proto
  // SetScheduleEnabled persists a loop's schedule-enabled flag,
  // independent of its continuous enabled flag. It does not itself
  // start/stop any run.
  rpc SetScheduleEnabled(SetScheduleEnabledRequest) returns (SetScheduleEnabledResponse);
```

Update `LoopInfo` to add two fields:

```proto
message LoopInfo {
  string name = 1;
  string path = 2;
  bool enabled = 3;
  repeated string steps = 4;
  string run_id = 5; // active run id, empty if not running
  bool schedule_enabled = 6;
  string next_run = 7; // RFC3339, empty if unscheduled or schedule disabled
}
```

Add the new request/response messages after `DeleteLoopResponse`:

```proto
message SetScheduleEnabledRequest {
  string loop_name = 1;
  string base_dir = 2;
  string workdir = 3;
  bool enabled = 4;
}
message SetScheduleEnabledResponse {}
```

- [ ] **Step 2: Regenerate the Go gRPC code**

Run: `./scripts/gen-proto.sh`
Expected: prints `generated rpc/*.pb.go`; `git status` shows `rpc/looper.pb.go` and `rpc/looper_grpc.pb.go` modified.

- [ ] **Step 3: Confirm the build picks up the new types**

Run: `go build ./...`
Expected: succeeds (this alone doesn't yet touch `daemon/service.go`, so it should already build — this step is a quick sanity check before wiring the handler).

- [ ] **Step 4: Write the failing test**

Add to `daemon/service_test.go` (check its existing helper names first — e.g. however it constructs a `*Server`/`*Manager` pair — and follow that same pattern; if it uses a `newTestServer(t)` helper, reuse it):

```go
func TestServer_SetScheduleEnabled(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:     "a",
		Schedule: &config.Schedule{Every: "1h"},
		Steps:    []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	baseDir := writeLoopsDir(t, dir, loop)

	srv := NewWithGlobal(nil, "looper")
	ctx := context.Background()

	if _, err := srv.SetScheduleEnabled(ctx, &rpc.SetScheduleEnabledRequest{
		LoopName: "a", BaseDir: baseDir, Workdir: dir, Enabled: false,
	}); err != nil {
		t.Fatalf("SetScheduleEnabled: %v", err)
	}

	resp, err := srv.ListLoops(ctx, &rpc.ListLoopsRequest{BaseDir: baseDir})
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	if len(resp.GetLoops()) != 1 || resp.GetLoops()[0].GetScheduleEnabled() {
		t.Errorf("loops = %v, want one loop with ScheduleEnabled=false", resp.GetLoops())
	}
}
```

(Match this test's imports/style to whatever `daemon/service_test.go` already imports — it will already import `context`, `testing`, `rpc`, and likely `config` given the other catalog-style RPC tests in that file; add only what's missing.)

- [ ] **Step 5: Run the test to verify it fails**

Run: `go test ./daemon/... -run TestServer_SetScheduleEnabled -v`
Expected: FAIL — `srv.SetScheduleEnabled` undefined, and `ListLoopInfo` (or wherever `ListLoops`'s response is built) has no `ScheduleEnabled` accessor yet.

- [ ] **Step 6: Add the handler and update `ListLoops`'s mapping in `daemon/service.go`**

Add `"time"` to the import block. Add the new handler right after `DeleteLoop`:

```go
// SetScheduleEnabled persists a loop's schedule-enabled flag. It never
// starts or stops a run itself.
func (s *Server) SetScheduleEnabled(ctx context.Context, req *rpc.SetScheduleEnabledRequest) (*rpc.SetScheduleEnabledResponse, error) {
	if err := s.manager.SetScheduleEnabled(req.GetLoopName(), req.GetBaseDir(), req.GetWorkdir(), req.GetEnabled()); err != nil {
		return nil, err
	}
	return &rpc.SetScheduleEnabledResponse{}, nil
}
```

Update `ListLoops`'s response-building loop to carry the two new fields — replace:

```go
		out[i] = &rpc.LoopInfo{Name: l.Name, Path: l.Path, Enabled: l.Enabled, Steps: l.Steps, RunId: l.RunID}
```

with:

```go
		var nextRun string
		if !l.NextRun.IsZero() {
			nextRun = l.NextRun.Format(time.RFC3339)
		}
		out[i] = &rpc.LoopInfo{
			Name: l.Name, Path: l.Path, Enabled: l.Enabled, Steps: l.Steps, RunId: l.RunID,
			ScheduleEnabled: l.ScheduleEnabled, NextRun: nextRun,
		}
```

- [ ] **Step 7: Run the test to verify it passes**

Run: `go test ./daemon/... -run TestServer_SetScheduleEnabled -v`
Expected: PASS.

- [ ] **Step 8: Run the full test suite and build**

Run: `go build ./... && go test ./...`
Expected: PASS, no build errors.

- [ ] **Step 9: Commit**

```bash
git add proto/looper.proto rpc/looper.pb.go rpc/looper_grpc.pb.go daemon/service.go daemon/service_test.go
git commit -m "feat: add SetScheduleEnabled RPC and schedule fields on LoopInfo"
```

---

### Task 8: TUI — schedule state in `LoopSnapshot`, toggle keybind, rendering

**Files:**
- Modify: `tui/model.go`
- Test: `tui/model_test.go`, `tui/view_test.go`

**Interfaces:**
- Produces: `LoopSnapshot.ScheduleEnabled bool`, `LoopSnapshot.NextRun string`; `Options.SetScheduleEnabledFn func(loopName string, enabled bool) tea.Cmd` — consumed by Task 9 (`program.go` wiring).

- [ ] **Step 1: Write the failing tests**

Add to `tui/view_test.go` (following the existing `TestView_ToggleEnabledKeyInvokesSetLoopEnabledFn` / `TestView_FleetShowsLoopsSection` patterns):

```go
func TestView_FleetShowsScheduleInfo(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(LoopsSnapshotMsg{
		{Name: "nightly", ScheduleEnabled: true, NextRun: "2026-07-17T21:00:00Z"},
	})
	m = next.(Model)

	out := m.View()
	if !strings.Contains(out, "2026-07-17T21:00:00Z") {
		t.Errorf("View() = %q, want it to show the loop's NextRun", out)
	}
}

func TestView_ToggleScheduleKeyInvokesSetScheduleEnabledFn(t *testing.T) {
	var gotName string
	var gotEnabled bool
	m := NewModel(Options{
		SetScheduleEnabledFn: func(name string, enabled bool) tea.Cmd {
			gotName, gotEnabled = name, enabled
			return nil
		},
	})
	next, _ := m.Update(LoopsSnapshotMsg{{Name: "a", ScheduleEnabled: false}})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // loop-row keys require the Loops tree focused
	m = next.(Model)

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if gotName != "a" || gotEnabled != true {
		t.Errorf("SetScheduleEnabledFn called with (%q, %v), want (\"a\", true)", gotName, gotEnabled)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./tui/... -run 'TestView_FleetShowsScheduleInfo|TestView_ToggleScheduleKeyInvokesSetScheduleEnabledFn' -v`
Expected: FAIL — `LoopSnapshot.ScheduleEnabled`/`NextRun` and `Options.SetScheduleEnabledFn` undefined.

- [ ] **Step 3: Add the fields, option, keybind, and rendering in `tui/model.go`**

Update `LoopSnapshot`:

```go
// LoopSnapshot is a point-in-time view of one configured loop, as returned
// by the daemon's ListLoops RPC.
type LoopSnapshot struct {
	Name    string
	Path    string
	Enabled bool
	Steps   []string
	RunID   string
	// ScheduleEnabled is true only when the loop has a schedule and it is
	// currently toggled on.
	ScheduleEnabled bool
	// NextRun is the loop's next scheduled firing time (RFC3339), or ""
	// if it has no schedule or ScheduleEnabled is false.
	NextRun string
}
```

Add to `Options`, right after `SetLoopEnabledFn`:

```go
	// SetScheduleEnabledFn toggles a loop's schedule-enabled state,
	// independent of SetLoopEnabledFn's continuous-run toggle.
	SetScheduleEnabledFn func(loopName string, enabled bool) tea.Cmd
```

In `handleLoopRowKey`, add a case alongside `"t"` (right after the `"t"` case's closing brace):

```go
	case "s":
		if m.opts.SetScheduleEnabledFn != nil {
			return m, m.opts.SetScheduleEnabledFn(loop.Name, !loop.ScheduleEnabled)
		}
```

Update its doc comment to mention the new key:

```go
// handleLoopRowKey implements the Loops-section loop-row action keys: t
// toggles enabled, s toggles the schedule, o runs once, g gracefully stops
// an active run, x hard-aborts one, R begins a rename, D begins a delete
// confirmation. All are no-ops when the cursor isn't on a loop row, and
// g/x are additionally no-ops when that loop has no active run.
```

In `viewFleet`, update the loop-row rendering block to append schedule info after `running`:

```go
				running := ""
				if loop.RunID != "" {
					running = fmt.Sprintf("  running (%s)", loop.RunID)
				}
				sched := ""
				if loop.ScheduleEnabled && loop.NextRun != "" {
					sched = fmt.Sprintf("  sched:next %s", loop.NextRun)
				} else if loop.ScheduleEnabled {
					sched = "  sched:on"
				}
				fmt.Fprintf(&b, "%s%-20s %s%s%s\n", cursor, loop.Name, status, running, sched)
```

Update the key hint line at the bottom of `viewFleet` to mention `s`:

```go
	b.WriteString("\n" + style.KeyHint.Render("[up/down] move  [tab] switch focus  [space] expand/collapse  [enter] focus  [t] toggle  [s] toggle schedule  [o] run once  [g] graceful stop  [x] abort  [R] rename  [D] delete  [n] new loop  [q] quit") + "\n")
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tui/... -run 'TestView_FleetShowsScheduleInfo|TestView_ToggleScheduleKeyInvokesSetScheduleEnabledFn' -v`
Expected: PASS.

- [ ] **Step 5: Run the full tui package test suite**

Run: `go test ./tui/... -v`
Expected: PASS (including every pre-existing `TestView_*`/`TestModel_*` test — confirms the key-hint text change and row-rendering change didn't break an exact-match assertion elsewhere; if any test asserts the full key-hint string verbatim, update it to include `[s] toggle schedule` in the same place this step put it).

- [ ] **Step 6: Commit**

```bash
git add tui/model.go tui/model_test.go tui/view_test.go
git commit -m "feat: TUI schedule toggle keybind and next-run display"
```

---

### Task 9: Wire `SetScheduleEnabledFn` and schedule fields in `tui/program.go`

**Files:**
- Modify: `tui/program.go`
- Test: `tui/program_test.go`

**Interfaces:**
- Consumes: `Options.SetScheduleEnabledFn`, `LoopSnapshot.ScheduleEnabled`/`NextRun` (Task 8); `rpc.LooperClient.SetScheduleEnabled`, `rpc.LoopInfo.ScheduleEnabled`/`NextRun` (Task 7).

- [ ] **Step 1: Write the failing test**

Add to `tui/program_test.go` (match its existing style for testing a `loopsSnapshotFromProto`-style pure function — look for how `TestLoopsSnapshotFromProto`-ish tests, if any, are structured; otherwise follow this shape):

```go
func TestLoopsSnapshotFromProto_MapsScheduleFields(t *testing.T) {
	loops := []*rpc.LoopInfo{
		{Name: "a", ScheduleEnabled: true, NextRun: "2026-07-17T21:00:00Z"},
	}
	got := loopsSnapshotFromProto(loops)
	if len(got) != 1 || !got[0].ScheduleEnabled || got[0].NextRun != "2026-07-17T21:00:00Z" {
		t.Errorf("loopsSnapshotFromProto = %+v, want ScheduleEnabled=true and NextRun preserved", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./tui/... -run TestLoopsSnapshotFromProto_MapsScheduleFields -v`
Expected: FAIL — `loopsSnapshotFromProto` doesn't yet map the new fields (compiles, but the assertion fails since `ScheduleEnabled`/`NextRun` stay zero-valued).

- [ ] **Step 3: Update `tui/program.go`**

Update `loopsSnapshotFromProto`:

```go
func loopsSnapshotFromProto(loops []*rpc.LoopInfo) LoopsSnapshotMsg {
	out := make(LoopsSnapshotMsg, 0, len(loops))
	for _, l := range loops {
		out = append(out, LoopSnapshot{
			Name: l.GetName(), Path: l.GetPath(), Enabled: l.GetEnabled(),
			Steps: l.GetSteps(), RunID: l.GetRunId(),
			ScheduleEnabled: l.GetScheduleEnabled(), NextRun: l.GetNextRun(),
		})
	}
	return out
}
```

Add a `setScheduleEnabledFn` helper right after `setLoopEnabledFn`:

```go
// setScheduleEnabledFn returns the Options.SetScheduleEnabledFn implementation.
func setScheduleEnabledFn(ctx context.Context, cl rpc.LooperClient, baseDir, workdir string) func(string, bool) tea.Cmd {
	return func(loopName string, enabled bool) tea.Cmd {
		return func() tea.Msg {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			_, err := cl.SetScheduleEnabled(rctx, &rpc.SetScheduleEnabledRequest{
				LoopName: loopName, BaseDir: baseDir, Workdir: workdir, Enabled: enabled,
			})
			if err != nil {
				return ErrMsg{Err: err}
			}
			return listLoopsFn(ctx, cl, baseDir)()()
		}
	}
}
```

Wire it into `Run`'s `Options` literal, right after `SetLoopEnabledFn`:

```go
		SetLoopEnabledFn:     setLoopEnabledFn(ctx, cl, baseDir, wd),
		SetScheduleEnabledFn: setScheduleEnabledFn(ctx, cl, baseDir, wd),
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./tui/... -run TestLoopsSnapshotFromProto_MapsScheduleFields -v`
Expected: PASS.

- [ ] **Step 5: Run the full build and test suite**

Run: `go build ./... && go test ./...`
Expected: PASS, no build errors.

- [ ] **Step 6: Commit**

```bash
git add tui/program.go tui/program_test.go
git commit -m "feat: wire schedule toggle and fields through the TUI's daemon client"
```

---

### Task 10: Document the `schedule` field in the README

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add the `schedule` block to the Loop YAML schema example**

In the "## Loop YAML schema" section, add a `schedule:` example right after `task_var` and before `steps:`:

```yaml
schedule:                 # optional repeating trigger; exactly one of every/at/cron
  every: 15m               # or: "2h", "1h30m" — any time.ParseDuration string
  # at: ["09:00", "14:00", "20:00"]   # daily times, 24-hour HH:MM
  # cron: "0 9 * * 1-5"               # raw 5-field cron expression
```

- [ ] **Step 2: Add a short prose paragraph after the schema block**

Right after the existing "Step types: ..." paragraph, add:

```markdown
A loop with a `schedule` is fired by looperd itself, as a fresh one-off run
(like `RunLoopOnce`), each time the schedule ticks — for as long as
looperd is running. If a run is already active for that loop when a tick
fires, the tick is skipped rather than stacked. Schedules use the daemon
process's local time zone and don't catch up on ticks missed while the
daemon was down.
```

- [ ] **Step 3: Verify the README renders sensibly**

Run: `cat README.md | sed -n '120,145p'`
Expected: the new `schedule:` block and paragraph appear in the right place, matching the existing formatting style.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: document the loop schedule field"
```

---

## Final verification (after all tasks)

- [ ] Run `go build ./... && go vet ./... && go test ./...` from the repo root — all must pass.
- [ ] Manually inspect a real loop file with `schedule: {every: 10s}` running against a local `looperd` (`looper daemon` in one terminal, `looper tui` — or whatever the fleet TUI entrypoint is named — in another) and confirm the loop fires roughly every 10s without any client re-issuing `RunLoopOnce`.
- [ ] Open the PR into `main` per CLAUDE.md's workflow rule (branch already in use for this work; do not merge directly to `main`).
