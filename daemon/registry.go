// Package daemon: registry.go implements the daemon-wide enabled-loops
// registry. looperd is a single per-user process shared across however
// many project directories invoke it (see client.SocketPath's per-uid
// path), so a per-project state file would be invisible to the daemon on
// its own restart. Instead, one registry file — resolved the same way as
// the socket path — tracks every (base_dir, loop_name) pair's enabled
// flag, so AutoResume can restart them without discovering project
// directories.
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
}

// registryFile is registry.json's on-disk shape.
type registryFile struct {
	Loops map[string]registryEntry `json:"loops"`
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

// loadRegistry reads and parses the registry file at path, returning an
// empty map (not an error) if the file doesn't exist yet.
func loadRegistry(path string) (map[string]registryEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]registryEntry{}, nil
		}
		return nil, fmt.Errorf("read registry %q: %w", path, err)
	}
	var rf registryFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parse registry %q: %w", path, err)
	}
	if rf.Loops == nil {
		rf.Loops = map[string]registryEntry{}
	}
	return rf.Loops, nil
}

// saveRegistry writes entries to path as JSON, creating any missing parent
// directories.
func saveRegistry(path string, entries map[string]registryEntry) error {
	data, err := json.MarshalIndent(registryFile{Loops: entries}, "", "  ")
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
