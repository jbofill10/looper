package history

import (
	"os"
	"path/filepath"
	"testing"
)

func writeIteration(t *testing.T, dir string, progressDone bool, lastEvent string, digestSteps ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "steps"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "context.json"), []byte(`{"vars":{}}`), 0o644); err != nil {
		t.Fatalf("write context.json: %v", err)
	}
	progress := `{"completed":[],"done":false}`
	if progressDone {
		progress = `{"completed":[],"done":true}`
	}
	if err := os.WriteFile(filepath.Join(dir, "progress.json"), []byte(progress), 0o644); err != nil {
		t.Fatalf("write progress.json: %v", err)
	}
	if lastEvent != "" {
		line := `{"step":"s","kind":"outcome","message":"` + lastEvent + `"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(line), 0o644); err != nil {
			t.Fatalf("write events.jsonl: %v", err)
		}
	}
	for _, name := range digestSteps {
		if err := os.WriteFile(filepath.Join(dir, "steps", name+".digest.md"), []byte("# "+name), 0o644); err != nil {
			t.Fatalf("write digest: %v", err)
		}
	}
}

func TestScan_FindsIterationsAcrossWorkerSubdirs(t *testing.T) {
	base := t.TempDir()
	writeIteration(t, filepath.Join(base, "runs", "loop1", "w1", "20260101T000000-001"), true, "advance", "get-tasks")
	writeIteration(t, filepath.Join(base, "runs", "loop1", "w2", "20260102T000000-001"), false, "no-work")

	entries, err := Scan(base, "loop1", []string{"get-tasks"})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].IterationID != "20260102T000000-001" {
		t.Errorf("entries[0].IterationID = %q, want newest first", entries[0].IterationID)
	}
	if entries[0].WorkerID != "w2" {
		t.Errorf("entries[0].WorkerID = %q, want w2", entries[0].WorkerID)
	}
	if entries[0].Status != "no-work" {
		t.Errorf("entries[0].Status = %q, want no-work", entries[0].Status)
	}
	if entries[1].Status != "done" {
		t.Errorf("entries[1].Status = %q, want done", entries[1].Status)
	}
	if !entries[1].Steps[0].HasDigest {
		t.Errorf("entries[1].Steps[0].HasDigest = false, want true")
	}
}

func TestScan_StatusRunningWhenNoTerminalEventOrDone(t *testing.T) {
	base := t.TempDir()
	writeIteration(t, filepath.Join(base, "runs", "loop1", "w1", "20260101T000000-001"), false, "")

	entries, err := Scan(base, "loop1", nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 1 || entries[0].Status != "running" {
		t.Fatalf("entries = %+v, want one running entry", entries)
	}
}

func TestScan_StatusAborted(t *testing.T) {
	base := t.TempDir()
	writeIteration(t, filepath.Join(base, "runs", "loop1", "w1", "20260101T000000-001"), false, "abort")

	entries, err := Scan(base, "loop1", nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 1 || entries[0].Status != "aborted" {
		t.Fatalf("entries = %+v, want one aborted entry", entries)
	}
}

func TestScan_NoWorkerSubdirLayout(t *testing.T) {
	base := t.TempDir()
	writeIteration(t, filepath.Join(base, "runs", "loop1", "20260101T000000-001"), true, "advance")

	entries, err := Scan(base, "loop1", nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].WorkerID != "" {
		t.Errorf("WorkerID = %q, want empty (no worker subdir)", entries[0].WorkerID)
	}
}

func TestScan_MissingRunsDirReturnsEmpty(t *testing.T) {
	base := t.TempDir()
	entries, err := Scan(base, "no-such-loop", nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestDigest_ReturnsContent(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "iter")
	writeIteration(t, dir, true, "advance", "get-tasks")

	content, err := Digest(dir, "get-tasks")
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if content != "# get-tasks" {
		t.Errorf("content = %q, want %q", content, "# get-tasks")
	}
}

func TestDigest_MissingFileReturnsEmptyNoError(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "iter")
	writeIteration(t, dir, true, "advance")

	content, err := Digest(dir, "no-such-step")
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if content != "" {
		t.Errorf("content = %q, want empty", content)
	}
}
