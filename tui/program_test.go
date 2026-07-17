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

func TestLoopsSnapshotFromProto(t *testing.T) {
	resp := []*rpc.LoopInfo{
		{Name: "a", Path: "/x/a.yaml", Enabled: true, Steps: []string{"s1", "s2"}, RunId: "run-001"},
	}
	got := loopsSnapshotFromProto(resp)
	want := LoopsSnapshotMsg{{Name: "a", Path: "/x/a.yaml", Enabled: true, Steps: []string{"s1", "s2"}, RunID: "run-001"}}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want[0]) {
		t.Errorf("loopsSnapshotFromProto = %+v, want %+v", got, want)
	}
}

func TestLoopsSnapshotFromProto_MapsScheduleFields(t *testing.T) {
	loops := []*rpc.LoopInfo{
		{Name: "a", ScheduleEnabled: true, NextRun: "2026-07-17T21:00:00Z"},
	}
	got := loopsSnapshotFromProto(loops)
	if len(got) != 1 || !got[0].ScheduleEnabled || got[0].NextRun != "2026-07-17T21:00:00Z" {
		t.Errorf("loopsSnapshotFromProto = %+v, want ScheduleEnabled=true and NextRun preserved", got)
	}
}
