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
