// Package daemon: schedule.go implements looperd's cron-style loop
// scheduling. A background rescan loop treats each known project's
// loops/*.yaml files as the source of truth for schedules (consistent
// with how ListLoops already scans the filesystem), diffing them against
// currently-registered cron entries and adding/updating/removing as
// needed. A firing triggers the same one-off run RunLoopOnce uses, unless
// the loop already has an active run.
package daemon

import (
	"log"
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
	if _, err := m.RunLoopOnce(loopName, "", baseDir, workdirFromBaseDir(baseDir)); err != nil {
		log.Printf("schedule fire %s/%s: %v", baseDir, loopName, err)
	}
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
