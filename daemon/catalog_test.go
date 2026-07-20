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
	runID, err := m.SetLoopEnabled("a", baseDir, dir, true)
	if err != nil {
		t.Fatalf("SetLoopEnabled: %v", err)
	}
	// The loop file has no max_iterations, so the run started above loops
	// forever until stopped; cancel it before the test's t.TempDir() cleanup
	// runs, or its background worker can race that cleanup.
	defer m.StopLoop(runID)

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
	runID, err := m.SetLoopEnabled("a", baseDir, dir, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	// The interactive step never signals a decision in this test, so the run
	// blocks indefinitely until cancelled; stop it before t.TempDir() cleanup
	// runs, or its background worker can race that cleanup.
	defer m.StopLoop(runID)

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
	// MaxIterations is bounded to 1 so the resumed run reaches run_done on
	// its own; an unbounded loop here would never stop (AutoResume doesn't
	// call StopLoopGraceful), hanging drainUntilRunDone forever.
	loop := &config.Loop{Name: "a", MaxIterations: 1, Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}}}
	baseDir := writeLoopsDir(t, dir, loop)

	registryPath := filepath.Join(t.TempDir(), "state.json")
	seed := NewManager(nil, "looper")
	seed.SetRegistryPath(registryPath)
	seedCh, seedUnsub := seed.Subscribe("")
	if _, err := seed.SetLoopEnabled("a", baseDir, dir, true); err != nil {
		t.Fatalf("seed enable: %v", err)
	}
	// Wait for the seed run to fully finish before proceeding: it shares
	// dir/baseDir with the run AutoResume starts below, and an in-flight
	// seed run racing t.TempDir()'s cleanup can trip "directory not empty".
	drainUntilRunDone(t, seedCh)
	seedUnsub()

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
