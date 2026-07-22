package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jbofill10/looper/history"
)

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

	// run_done removes run-1's workers from the fleet view entirely: a
	// finished run's history belongs in viewRuns, not lingering here.
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "run_done", LoopName: "loopA", State: "done"})
	for _, r := range m.Workers() {
		if r.RunID == "run-1" {
			t.Fatalf("run-1 worker %s still present after run_done, want removed: %+v", r.WorkerID, r)
		}
	}
	if got := len(m.Workers()); got != 2 {
		t.Fatalf("Workers() count after run-1 run_done = %d, want 2 (run-2 only)", got)
	}
}

// TestModel_FinishedWorkersExcludedFromFleetView asserts that a worker row
// whose Status is done/stopped/error is hidden from Workers() (the fleet
// view), unless it has a pending decision request awaiting a response.
func TestModel_FinishedWorkersExcludedFromFleetView(t *testing.T) {
	m := NewModel(Options{})
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "step_start", LoopName: "loopA", WorkerID: "w1", Task: "t", Step: "s"})
	m = send(t, m, StateUpdateMsg{RunID: "run-2", Kind: "step_start", LoopName: "loopB", WorkerID: "w1", Task: "t", Step: "s"})

	for _, status := range []string{"done", "stopped", "error"} {
		m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "run_done", LoopName: "loopA", State: status})
		rows := m.Workers()
		if len(rows) != 1 || rows[0].RunID != "run-2" {
			t.Fatalf("Workers() with run-1 status %q = %+v, want only run-2's row", status, rows)
		}
	}
}

// TestModel_RunDoneDoesNotLeaveStaleDuplicateStepRows is a regression test
// for a fleet-view bug: a finished run's rows lingered in Model.workers
// forever (never evicted), so a later run reusing the same step names
// appeared to duplicate every step — one stale "done" row next to the
// active run's row for that same step. run_done must remove the finished
// run's rows outright.
func TestModel_RunDoneDoesNotLeaveStaleDuplicateStepRows(t *testing.T) {
	m := NewModel(Options{})

	// run-1 (concurrency=1, WorkerID "") runs through get-tasks/pick-task
	// and finishes.
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "step_start", LoopName: "loopA", Step: "get-tasks", Task: "t"})
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "step_start", LoopName: "loopA", Step: "pick-task", Task: "t"})
	m = send(t, m, StateUpdateMsg{RunID: "run-1", Kind: "run_done", LoopName: "loopA", State: "done"})

	// run-2 (concurrency>=1, worker w1) starts on the very same steps.
	m = send(t, m, StateUpdateMsg{RunID: "run-2", Kind: "step_start", LoopName: "loopA", WorkerID: "w1", Step: "get-tasks", Task: "t"})

	rows := m.Workers()
	if len(rows) != 1 {
		t.Fatalf("Workers() = %+v, want exactly 1 row (run-1 should have been evicted by run_done)", rows)
	}
	if rows[0].RunID != "run-2" {
		t.Fatalf("remaining row = %+v, want run-2's row", rows[0])
	}
}

// TestModel_RunsSnapshotSkipsTerminalRuns is a regression test for the same
// stale-row bug via the startup ListRuns snapshot path: the daemon never
// evicts finished runs, so a snapshot can include runs that already reached
// a terminal status. applyRunsSnapshot must not populate (or must evict)
// rows for those, or they'd sit in the fleet view forever alongside later
// active runs on the same steps.
func TestModel_RunsSnapshotSkipsTerminalRuns(t *testing.T) {
	m := NewModel(Options{})
	m = send(t, m, StateUpdateMsg{RunID: "run-2", Kind: "step_start", LoopName: "loopA", WorkerID: "w1", Step: "get-tasks", Task: "t"})

	updated, _ := m.Update(RunsSnapshotMsg{
		{RunID: "run-1", LoopName: "loopA", Status: "done", Workers: []WorkerSnapshot{
			{WorkerID: "", CurrentStep: "get-tasks", Status: "done"},
		}},
		{RunID: "run-2", LoopName: "loopA", Status: "running", Workers: []WorkerSnapshot{
			{WorkerID: "w1", CurrentStep: "get-tasks", Status: "running"},
		}},
	})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}

	rows := m.Workers()
	if len(rows) != 1 {
		t.Fatalf("Workers() = %+v, want exactly 1 row (terminal run-1 should be skipped/evicted)", rows)
	}
	if rows[0].RunID != "run-2" {
		t.Fatalf("remaining row = %+v, want run-2's row", rows[0])
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

func TestModel_LoopsSnapshotPopulatesTreeRows(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(LoopsSnapshotMsg{
		{Name: "a", Steps: []string{"s1", "s2"}},
		{Name: "b", Steps: []string{"s3"}},
	})
	m = next.(Model)

	rows := m.treeRows()
	if len(rows) != 2 {
		t.Fatalf("treeRows (collapsed) = %v, want 2 loop rows", rows)
	}
	if rows[0].Kind != "loop" || rows[0].LoopName != "a" {
		t.Errorf("rows[0] = %+v, want loop row for a", rows[0])
	}
}

func TestModel_ExpandingLoopShowsItsSteps(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(LoopsSnapshotMsg{
		{Name: "a", Steps: []string{"s1", "s2"}},
	})
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(Model)

	rows := m.treeRows()
	if len(rows) != 3 {
		t.Fatalf("treeRows (expanded) = %v, want 1 loop row + 2 step rows", rows)
	}
	if rows[1].Kind != "step" || rows[1].LoopName != "a" || rows[1].StepIndex != 0 {
		t.Errorf("rows[1] = %+v, want step row (a, 0)", rows[1])
	}
	if rows[2].Kind != "step" || rows[2].StepIndex != 1 {
		t.Errorf("rows[2] = %+v, want step row (a, 1)", rows[2])
	}
}

func TestModel_UpDownMovesWorkersCursorByDefault(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(LoopsSnapshotMsg{{Name: "a"}, {Name: "b"}})
	m = next.(Model)
	next, _ = m.Update(StateUpdateMsg{RunID: "r1", WorkerID: "w1", Kind: "state"})
	m = next.(Model)
	next, _ = m.Update(StateUpdateMsg{RunID: "r1", WorkerID: "w2", Kind: "state"})
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.cursor != 1 {
		t.Errorf("cursor (Workers) = %d, want 1 (down must move the Workers cursor by default, unchanged from today's behavior)", m.cursor)
	}
	if m.treeCursor != 0 {
		t.Errorf("treeCursor = %d, want unchanged at 0 while Workers has default focus", m.treeCursor)
	}
}

func TestModel_TabSwitchesFocusToLoopsTree(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(LoopsSnapshotMsg{{Name: "a"}, {Name: "b"}})
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if !m.loopsFocused {
		t.Fatalf("tab did not switch focus to the Loops tree")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.treeCursor != 1 {
		t.Errorf("treeCursor = %d, want 1 (down should move the tree cursor once tab has focused it)", m.treeCursor)
	}
	if m.cursor != 0 {
		t.Errorf("cursor (Workers) = %d, want unchanged at 0 while the Loops tree has focus", m.cursor)
	}
}

// TestModel_ErrMsgSurfacesInBuilderMsg guards against RPC errors (e.g. the
// by-design rejection when renaming/deleting a loop with an active run)
// being silently dropped: Update must store the error where viewFleet
// renders it, following the existing "error:"-prefixed builderMsg
// convention.
func TestModel_ErrMsgSurfacesInBuilderMsg(t *testing.T) {
	m := NewModel(Options{})
	next, cmd := m.Update(ErrMsg{Err: errors.New("loop \"a\" has an active run (run-1); stop it before renaming")})
	m = next.(Model)

	if cmd != nil {
		t.Errorf("Update(ErrMsg) cmd = %v, want nil", cmd)
	}
	if !strings.HasPrefix(m.builderMsg, "error:") {
		t.Fatalf("builderMsg = %q, want it to start with %q", m.builderMsg, "error:")
	}
	if !strings.Contains(m.builderMsg, "active run") {
		t.Errorf("builderMsg = %q, want it to contain the underlying error text", m.builderMsg)
	}
}

func TestHandleLoopRowKey_HTriggersLoadHistoryAndOpensViewRuns(t *testing.T) {
	var gotLoopName string
	m := NewModel(Options{
		LoadHistoryFn: func(loopName string) tea.Cmd {
			gotLoopName = loopName
			return func() tea.Msg { return nil }
		},
	})
	mm, _ := m.Update(LoopsSnapshotMsg{{Name: "loop1", Enabled: true}})
	m = mm.(Model)
	m.loopsFocused = true

	m, cmd := press(t, m, "h")
	if cmd == nil {
		t.Fatalf("expected a command from LoadHistoryFn")
	}
	if gotLoopName != "loop1" {
		t.Errorf("LoadHistoryFn called with %q, want loop1", gotLoopName)
	}
	if m.view != viewRuns {
		t.Errorf("view = %v, want viewRuns", m.view)
	}
	if m.historyLoop != "loop1" {
		t.Errorf("historyLoop = %q, want loop1", m.historyLoop)
	}
}

func TestHistorySnapshotMsg_PopulatesHistoryForMatchingLoop(t *testing.T) {
	m := NewModel(Options{})
	m.historyLoop = "loop1"

	entries := []history.Entry{{IterationID: "iter-1", Status: "done"}}
	mm, _ := m.Update(HistorySnapshotMsg{LoopName: "loop1", Entries: entries})
	m = mm.(Model)
	if len(m.history) != 1 || m.history[0].IterationID != "iter-1" {
		t.Errorf("history = %+v, want one entry iter-1", m.history)
	}
}

func TestHistorySnapshotMsg_IgnoredForStaleLoop(t *testing.T) {
	m := NewModel(Options{})
	m.historyLoop = "loop1"

	mm, _ := m.Update(HistorySnapshotMsg{LoopName: "loop2", Entries: []history.Entry{{IterationID: "iter-1"}}})
	m = mm.(Model)
	if len(m.history) != 0 {
		t.Errorf("history = %+v, want empty (stale loop name)", m.history)
	}
}

func TestViewRuns_EnterOpensViewDigest(t *testing.T) {
	m := NewModel(Options{})
	m.view = viewRuns
	m.historyLoop = "loop1"
	m.history = []history.Entry{{IterationID: "iter-1", Steps: []history.StepDigest{{Name: "a", HasDigest: true}}}}

	m, _ = press(t, m, "enter")
	if m.view != viewDigest {
		t.Errorf("view = %v, want viewDigest", m.view)
	}
	if m.selectedRun.IterationID != "iter-1" {
		t.Errorf("selectedRun = %+v, want iter-1", m.selectedRun)
	}
}

func TestViewDigest_EnterOnStepWithDigestCallsLoadDigestFn(t *testing.T) {
	var gotStep string
	m := NewModel(Options{
		LoadDigestFn: func(loopName string, entry history.Entry, step string) tea.Cmd {
			gotStep = step
			return func() tea.Msg { return nil }
		},
	})
	m.view = viewDigest
	m.historyLoop = "loop1"
	m.selectedRun = history.Entry{IterationID: "iter-1", Steps: []history.StepDigest{{Name: "a", HasDigest: true}}}

	_, cmd := press(t, m, "enter")
	if cmd == nil {
		t.Fatalf("expected a command from LoadDigestFn")
	}
	if gotStep != "a" {
		t.Errorf("LoadDigestFn called with step %q, want a", gotStep)
	}
}

func TestViewDigest_EnterOnStepWithoutDigestIsNoop(t *testing.T) {
	called := false
	m := NewModel(Options{
		LoadDigestFn: func(loopName string, entry history.Entry, step string) tea.Cmd {
			called = true
			return nil
		},
	})
	m.view = viewDigest
	m.selectedRun = history.Entry{Steps: []history.StepDigest{{Name: "a", HasDigest: false}}}

	press(t, m, "enter")
	if called {
		t.Errorf("LoadDigestFn should not be called for a step with no digest")
	}
}

func TestDigestContentMsg_PopulatesContent(t *testing.T) {
	m := NewModel(Options{})
	mm, _ := m.Update(DigestContentMsg{Step: "a", Content: "# hi"})
	m = mm.(Model)
	if m.digestStep != "a" || m.digestContent != "# hi" {
		t.Errorf("digestStep/digestContent = %q/%q, want a/# hi", m.digestStep, m.digestContent)
	}
}

func TestEsc_UnwindsViewDigestToViewRunsToViewFleet(t *testing.T) {
	m := NewModel(Options{})
	m.view = viewDigest
	m, _ = press(t, m, "esc")
	if m.view != viewRuns {
		t.Errorf("view = %v, want viewRuns", m.view)
	}
	m, _ = press(t, m, "esc")
	if m.view != viewFleet {
		t.Errorf("view = %v, want viewFleet", m.view)
	}
	if m.historyLoop != "" {
		t.Errorf("historyLoop = %q, want empty after leaving viewRuns", m.historyLoop)
	}
}
