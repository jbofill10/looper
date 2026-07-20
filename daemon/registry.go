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
