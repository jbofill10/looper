package tui

import "testing"

// send applies msg to m and returns the updated Model (discarding the cmd,
// which Task 1's aggregation logic never needs).
func send(t *testing.T, m Model, msg StateUpdateMsg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return mm
}

func TestModel_WorkersAggregationAndOrdering(t *testing.T) {
	m := NewModel(Options{})

	// run-1: two workers, w1 becomes needs-human via decision_request.
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "step_start", LoopName: "loopA", WorkerID: "w1", Task: "task-a", Step: "build", Iteration: 1})
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "step_start", LoopName: "loopA", WorkerID: "w2", Task: "task-b", Step: "build", Iteration: 1})
	// run-2: two workers, none pending.
	m = send(t, m, StateUpdateMsg{RunID: "run-2", Kind: "step_start", LoopName: "loopB", WorkerID: "w1", Task: "task-c", Step: "test", Iteration: 1})
	m = send(t, m, StateUpdateMsg{RunID: "run-2", Kind: "step_start", LoopName: "loopB", WorkerID: "w2", Task: "task-d", Step: "test", Iteration: 1})

	if got := len(m.Workers()); got != 4 {
		t.Fatalf("Workers() count = %d, want 4", got)
	}
	if m.NeedYouCount() != 0 {
		t.Fatalf("NeedYouCount() = %d, want 0 before any decision_request", m.NeedYouCount())
	}

	// run-1/w1 gets a decision request: it must sort first and NeedYouCount
	// must become 1.
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "decision_request", LoopName: "loopA", WorkerID: "w1", Task: "task-a", Step: "build", RequestID: "req-1", Options: []string{"advance", "retry", "abort"}})

	if got := m.NeedYouCount(); got != 1 {
		t.Fatalf("NeedYouCount() = %d, want 1", got)
	}
	rows := m.Workers()
	if len(rows) != 4 {
		t.Fatalf("Workers() count = %d, want 4", len(rows))
	}
	first := rows[0]
	if first.RunID != "run-1" || first.WorkerID != "w1" {
		t.Fatalf("first row = %+v, want run-1/w1 sorted to top", first)
	}
	if first.PendingReqID != "req-1" {
		t.Fatalf("first row PendingReqID = %q, want req-1", first.PendingReqID)
	}
	if len(first.PendingOptions) != 3 || first.PendingOptions[0] != "advance" {
		t.Fatalf("first row PendingOptions = %v, want [advance retry abort]", first.PendingOptions)
	}

	// A later state update for the same worker clears the pending decision.
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "state", LoopName: "loopA", WorkerID: "w1", Task: "task-a", Step: "build", State: "ok"})
	if got := m.NeedYouCount(); got != 0 {
		t.Fatalf("NeedYouCount() after state update = %d, want 0", got)
	}
	for _, r := range m.Workers() {
		if r.RunID == "run-1" && r.WorkerID == "w1" && r.PendingReqID != "" {
			t.Fatalf("run-1/w1 PendingReqID = %q, want cleared", r.PendingReqID)
		}
	}

	// run_done marks run-1's workers done.
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "run_done", LoopName: "loopA", State: "done"})
	for _, r := range m.Workers() {
		if r.RunID == "run-1" && r.Status != "done" {
			t.Fatalf("run-1 worker %s Status = %q, want done", r.WorkerID, r.Status)
		}
	}
}

func TestModel_DecisionRequestKeyedByRunAndWorker(t *testing.T) {
	m := NewModel(Options{})
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "step_start", WorkerID: "w1", Task: "t1", Step: "s"})
	m = send(t, m, StateUpdateMsg{RunID: "run-2", Kind: "step_start", WorkerID: "w1", Task: "t2", Step: "s"})
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "decision_request", WorkerID: "w1", RequestID: "req-x", Options: []string{"advance"}})

	if got := m.NeedYouCount(); got != 1 {
		t.Fatalf("NeedYouCount() = %d, want 1", got)
	}
	for _, r := range m.Workers() {
		if r.RunID == "run-2" && r.PendingReqID != "" {
			t.Fatalf("run-2/w1 got a pending decision it never received: %+v", r)
		}
	}
}
