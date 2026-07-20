// Package history scans looper's on-disk run directories to build a loop's
// run history: one entry per iteration, with per-step digest presence, for
// the fleet TUI's run-history browsing (see tui.Model's viewRuns/viewDigest).
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// StepDigest names one step of a loop, in config order, and whether the
// iteration captured a digest for it (steps/<name>.digest.md exists).
type StepDigest struct {
	Name      string
	HasDigest bool
}

// Entry describes one iteration of a loop's run history.
type Entry struct {
	Dir         string // absolute path to the iteration directory
	IterationID string // e.g. "20260717T134719-001"
	WorkerID    string // "" if the loop has no per-worker run subdirs
	Status      string // "running" | "done" | "aborted" | "no-work"
	Steps       []StepDigest
}

// Scan walks <baseDir>/runs/<loopName>/ for iteration directories (an
// iteration directory is any directory containing context.json; this
// transparently handles both the runs/<loop>/<iter> and
// runs/<loop>/<worker>/<iter> layouts) and returns one Entry per iteration
// found, newest first. stepNames is the loop's steps in config order; each
// is checked for a captured digest in every iteration found. A missing runs
// directory is not an error — it yields an empty slice.
func Scan(baseDir, loopName string, stepNames []string) ([]Entry, error) {
	root := filepath.Join(baseDir, "runs", loopName)
	var entries []Entry

	var walk func(dir string)
	walk = func(dir string) {
		list, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		hasContext := false
		for _, e := range list {
			if !e.IsDir() && e.Name() == "context.json" {
				hasContext = true
				break
			}
		}
		if hasContext {
			entries = append(entries, buildEntry(root, dir, stepNames))
			return
		}
		for _, e := range list {
			if e.IsDir() {
				walk(filepath.Join(dir, e.Name()))
			}
		}
	}
	walk(root)

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].IterationID > entries[j].IterationID
	})
	return entries, nil
}

func buildEntry(root, dir string, stepNames []string) Entry {
	workerID := ""
	if parent := filepath.Dir(dir); parent != root {
		workerID = filepath.Base(parent)
	}
	steps := make([]StepDigest, len(stepNames))
	for i, name := range stepNames {
		_, err := os.Stat(filepath.Join(dir, "steps", name+".digest.md"))
		steps[i] = StepDigest{Name: name, HasDigest: err == nil}
	}
	return Entry{
		Dir:         dir,
		IterationID: filepath.Base(dir),
		WorkerID:    workerID,
		Status:      status(dir),
		Steps:       steps,
	}
}

// status derives an iteration's status from progress.json's Done flag and
// the last "outcome" event in events.jsonl.
func status(dir string) string {
	done := false
	if data, err := os.ReadFile(filepath.Join(dir, "progress.json")); err == nil {
		var p struct {
			Done bool `json:"done"`
		}
		if json.Unmarshal(data, &p) == nil {
			done = p.Done
		}
	}
	switch lastOutcome(dir) {
	case "abort":
		return "aborted"
	case "no-work":
		return "no-work"
	}
	if done {
		return "done"
	}
	return "running"
}

// lastOutcome returns the Message of the last Kind=="outcome" event in
// dir/events.jsonl (runctx.Event's JSON shape), or "" if none is found.
func lastOutcome(dir string) string {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()

	var last string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Kind == "outcome" {
			last = ev.Message
		}
	}
	return last
}

// Digest reads the captured digest content for one step of the iteration at
// dir (an Entry.Dir from Scan). It returns "" with no error if the step has
// no digest for this iteration.
func Digest(dir, stepName string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "steps", stepName+".digest.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read digest: %w", err)
	}
	return string(data), nil
}
