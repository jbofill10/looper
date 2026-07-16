// Package daemon: catalog.go implements the Loops-catalog operations the
// TUI's Loops tree drives: listing every configured loop alongside its
// enabled/running state, toggling enabled (persisted via the registry),
// running once, and rename/delete.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jbofill10/looper/config"
)

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
}

// ListLoops scans <baseDir>/loops/*.yaml and cross-references the
// registry (enabled flag) and active runs (RunID), sorted by loop name. A
// missing loops directory returns an empty slice, not an error.
func (m *Manager) ListLoops(baseDir string) ([]LoopSummary, error) {
	loopsDir := filepath.Join(baseDir, "loops")
	entries, err := os.ReadDir(loopsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading loops directory: %w", err)
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
		out = append(out, LoopSummary{
			Name:    loop.Name,
			Path:    path,
			Enabled: registry[registryKey(baseDir, loop.Name)].Enabled,
			Steps:   stepNames,
			RunID:   activeByLoop[loop.Name],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// isYAMLFile reports whether name has a .yaml or .yml extension.
func isYAMLFile(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// SetLoopEnabled persists loopName's enabled flag (keyed by base_dir) to
// the registry. Enabling a loop with no active run in baseDir starts one
// (via StartLoop) and returns its run id; enabling an already-running loop
// is a no-op returning its existing run id. Disabling a running loop
// triggers a graceful stop (StopLoopGraceful) and returns its run id;
// disabling an already-stopped loop returns "".
func (m *Manager) SetLoopEnabled(loopName, baseDir, workdir string, enabled bool) (string, error) {
	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		return "", err
	}
	key := registryKey(baseDir, loopName)
	registry[key] = registryEntry{BaseDir: baseDir, Workdir: workdir, LoopName: loopName, Enabled: enabled}
	if err := saveRegistry(m.registryPath, registry); err != nil {
		return "", err
	}

	runID := m.activeRun(baseDir, loopName)

	if enabled {
		if runID != "" {
			return runID, nil
		}
		return m.StartLoop(loopName, "", baseDir, workdir, 0)
	}
	if runID == "" {
		return "", nil
	}
	return runID, m.StopLoopGraceful(runID)
}

// activeRun returns the run id of loopName's active run in baseDir, or ""
// if it has none.
func (m *Manager) activeRun(baseDir, loopName string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, re := range m.runs {
		if re.baseDir == baseDir && re.info.LoopName == loopName && re.info.Status == "running" {
			return id
		}
	}
	return ""
}

// RunLoopOnce starts loopName as a one-off run with max_iterations forced
// to 1, independent of the loop file's own configured value. It does not
// touch the registry or the loop's enabled flag.
func (m *Manager) RunLoopOnce(loopName, loopFile, baseDir, workdir string) (string, error) {
	path := loopFile
	if path == "" {
		path = filepath.Join(baseDir, "loops", loopName+".yaml")
	}
	loop, err := config.LoadLoop(path)
	if err != nil {
		return "", err
	}
	once := *loop
	once.MaxIterations = 1

	tmp, err := os.MkdirTemp("", "looper-run-once-*")
	if err != nil {
		return "", fmt.Errorf("preparing run-once loop file: %w", err)
	}
	oncePath := filepath.Join(tmp, loopName+".yaml")
	if err := config.SaveLoop(&once, oncePath); err != nil {
		return "", fmt.Errorf("writing run-once loop file: %w", err)
	}
	return m.StartLoop("", oncePath, baseDir, workdir, 0)
}

// RenameLoop renames loopName's YAML file (updating its Name field) and
// its registry entry to newName. It returns an error if the loop
// currently has an active run in baseDir.
func (m *Manager) RenameLoop(loopName, newName, baseDir string) error {
	if runID := m.activeRun(baseDir, loopName); runID != "" {
		return fmt.Errorf("loop %q has an active run (%s); stop it before renaming", loopName, runID)
	}

	oldPath := filepath.Join(baseDir, "loops", loopName+".yaml")
	loop, err := config.LoadLoopLenient(oldPath)
	if err != nil {
		return err
	}
	loop.Name = newName
	newPath := filepath.Join(baseDir, "loops", newName+".yaml")
	if err := config.SaveLoop(loop, newPath); err != nil {
		return err
	}
	if err := os.Remove(oldPath); err != nil {
		return fmt.Errorf("removing old loop file: %w", err)
	}

	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		return err
	}
	oldKey := registryKey(baseDir, loopName)
	if entry, ok := registry[oldKey]; ok {
		delete(registry, oldKey)
		entry.LoopName = newName
		registry[registryKey(baseDir, newName)] = entry
		if err := saveRegistry(m.registryPath, registry); err != nil {
			return err
		}
	}
	return nil
}

// DeleteLoop removes loopName's YAML file and its registry entry (if any).
// It returns an error if the loop currently has an active run in baseDir.
func (m *Manager) DeleteLoop(loopName, baseDir string) error {
	if runID := m.activeRun(baseDir, loopName); runID != "" {
		return fmt.Errorf("loop %q has an active run (%s); stop it before deleting", loopName, runID)
	}

	path := filepath.Join(baseDir, "loops", loopName+".yaml")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing loop file: %w", err)
	}

	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		return err
	}
	key := registryKey(baseDir, loopName)
	if _, ok := registry[key]; ok {
		delete(registry, key)
		if err := saveRegistry(m.registryPath, registry); err != nil {
			return err
		}
	}
	return nil
}

// AutoResume starts every registry entry marked enabled, using its
// persisted base_dir/workdir. Called once at daemon startup. Errors (e.g.
// a loop file since deleted) are collected and returned rather than
// aborting the rest — one bad entry must not block every other loop from
// resuming.
func (m *Manager) AutoResume() []error {
	registry, err := loadRegistry(m.registryPath)
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, entry := range registry {
		if !entry.Enabled {
			continue
		}
		if _, err := m.StartLoop(entry.LoopName, "", entry.BaseDir, entry.Workdir, 0); err != nil {
			errs = append(errs, fmt.Errorf("auto-resume %q: %w", entry.LoopName, err))
		}
	}
	return errs
}
