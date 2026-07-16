package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoop_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loops", "dev-loop.yaml")

	l := &Loop{
		Name:        "dev-loop",
		Concurrency: 2,
		Steps: []Step{
			{
				Name:    "get-task",
				Type:    StepScript,
				Run:     "echo TASK_ID=1 >> $LOOPER_OUTPUT\necho DONE=1 >> $LOOPER_OUTPUT",
				Outputs: []string{"TASK_ID", "DONE"},
			},
			{
				Name: "review",
				Type: StepManual,
			},
		},
	}

	if err := SaveLoop(l, path); err != nil {
		t.Fatalf("SaveLoop: %v", err)
	}

	loaded, err := LoadLoop(path)
	if err != nil {
		t.Fatalf("LoadLoop: %v", err)
	}
	if loaded.Name != "dev-loop" {
		t.Errorf("name = %q, want dev-loop", loaded.Name)
	}
	if loaded.Concurrency != 2 {
		t.Errorf("concurrency = %d, want 2", loaded.Concurrency)
	}
	if len(loaded.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(loaded.Steps))
	}
	if loaded.Steps[0].Run != l.Steps[0].Run {
		t.Errorf("run = %q, want %q", loaded.Steps[0].Run, l.Steps[0].Run)
	}
	if len(loaded.Steps[0].Outputs) != 2 {
		t.Errorf("outputs = %v, want 2 entries", loaded.Steps[0].Outputs)
	}
	if loaded.Steps[1].Type != StepManual {
		t.Errorf("step1 type = %q, want manual", loaded.Steps[1].Type)
	}
}

func TestSaveLoop_InvalidNotWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loops", "bad-loop.yaml")

	l := &Loop{Name: "bad-loop"} // no steps

	err := SaveLoop(l, path)
	if err == nil {
		t.Fatal("SaveLoop: expected error for invalid loop, got nil")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("SaveLoop wrote a file for an invalid loop: %v", statErr)
	}
}
