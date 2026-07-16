package tui

import (
	"reflect"
	"testing"

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
