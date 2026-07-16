package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/rpc"
)

func TestUpdateFromProto(t *testing.T) {
	in := &rpc.StateUpdate{
		RunId:     "run-1",
		Kind:      "decision_request",
		LoopName:  "loopA",
		Iteration: 3,
		Step:      "build",
		State:     "failed",
		Message:   "exit 1",
		RequestId: "req-1",
		Options:   []string{"advance", "retry", "abort"},
		WorkerId:  "w1",
		Task:      "task-a",
	}

	got := updateFromProto(in)

	want := StateUpdateMsg{
		RunID:     "run-1",
		Kind:      "decision_request",
		LoopName:  "loopA",
		Iteration: 3,
		Step:      "build",
		State:     "failed",
		Message:   "exit 1",
		RequestID: "req-1",
		Options:   []string{"advance", "retry", "abort"},
		WorkerID:  "w1",
		Task:      "task-a",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updateFromProto = %+v, want %+v", got, want)
	}
}

func TestSaveLoopFn_WritesUnderLoopsDir(t *testing.T) {
	dir := t.TempDir()
	fn := saveLoopFn(dir)

	loop := &config.Loop{
		Name:  "dev-loop",
		Steps: []config.Step{{Name: "step-1", Type: config.StepManual}},
	}

	path, err := fn(loop)
	if err != nil {
		t.Fatalf("saveLoopFn returned error: %v", err)
	}
	want := filepath.Join(dir, ".looper", "loops", "dev-loop.yaml")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %q: %v", path, err)
	}
}

func TestSaveLoopFn_InvalidLoopReturnsError(t *testing.T) {
	dir := t.TempDir()
	fn := saveLoopFn(dir)

	if _, err := fn(&config.Loop{}); err == nil {
		t.Fatalf("expected error for loop with no name/steps")
	}
}
