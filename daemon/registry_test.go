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
