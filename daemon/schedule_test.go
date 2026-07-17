package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jbofill10/looper/config"
	"github.com/robfig/cron/v3"
)

type cronEntryIDSlice = cron.EntryID

func entryIDs(e scheduledEntry) []cron.EntryID { return e.ids }

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
