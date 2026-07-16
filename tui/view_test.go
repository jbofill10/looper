package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jbofill10/looper/builder"
)

// key builds a synthetic tea.KeyMsg for the given single-character or named
// key ("up", "down", "enter", "esc", "q", "a", "r", "x", "ctrl+c").
func key(k string) tea.KeyMsg {
	switch k {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

func press(t *testing.T, m Model, k string) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(key(k))
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return mm, cmd
}

func twoWorkerModel() Model {
	m := NewModel(Options{})
	m = func() Model {
		mm, _ := m.Update(StateUpdateMsg{RunID: "run-1", Kind: "step_start", LoopName: "loopA", WorkerID: "w1", Task: "task-a", Step: "build", State: "running"})
		return mm.(Model)
	}()
	m = func() Model {
		mm, _ := m.Update(StateUpdateMsg{RunID: "run-1", Kind: "step_start", LoopName: "loopA", WorkerID: "w2", Task: "task-b", Step: "test", State: "running"})
		return mm.(Model)
	}()
	return m
}

func TestView_FleetHeaderAndRows(t *testing.T) {
	m := twoWorkerModel()
	out := m.View()
	if !strings.Contains(out, "looper · 1 runs · 0 NEED YOU") {
		t.Fatalf("fleet view missing header badge:\n%s", out)
	}
	if !strings.Contains(out, "w1") || !strings.Contains(out, "task-a") {
		t.Fatalf("fleet view missing worker row:\n%s", out)
	}
	if !strings.Contains(out, "w2") || !strings.Contains(out, "task-b") {
		t.Fatalf("fleet view missing second worker row:\n%s", out)
	}
}

func TestView_NeedYouBadgeAndGlyph(t *testing.T) {
	m := twoWorkerModel()
	mm, _ := m.Update(StateUpdateMsg{RunID: "run-1", Kind: "decision_request", WorkerID: "w1", RequestID: "req-1", Options: []string{"advance", "retry", "abort"}})
	m = mm.(Model)
	out := m.View()
	if !strings.Contains(out, "1 NEED YOU") {
		t.Fatalf("fleet view missing NEED YOU badge:\n%s", out)
	}
	if !strings.Contains(out, "⏸") {
		t.Fatalf("fleet view missing needs-human glyph:\n%s", out)
	}
}

func TestView_CursorMovement(t *testing.T) {
	m := twoWorkerModel()
	out := m.View()
	lines := strings.Split(out, "\n")
	firstCursorLine := -1
	for i, l := range lines {
		if strings.Contains(l, "▸") {
			firstCursorLine = i
			break
		}
	}
	if firstCursorLine == -1 {
		t.Fatalf("no cursor glyph found in initial view:\n%s", out)
	}
	if !strings.Contains(lines[firstCursorLine], "w1") {
		t.Fatalf("cursor not on first row initially:\n%s", out)
	}

	m, _ = press(t, m, "down")
	out2 := m.View()
	lines2 := strings.Split(out2, "\n")
	movedCursorLine := -1
	for i, l := range lines2 {
		if strings.Contains(l, "▸") {
			movedCursorLine = i
			break
		}
	}
	if movedCursorLine == -1 || !strings.Contains(lines2[movedCursorLine], "w2") {
		t.Fatalf("cursor did not move to second row after 'down':\n%s", out2)
	}
}

func TestView_EnterSwitchesToFocus(t *testing.T) {
	m := twoWorkerModel()
	m, _ = press(t, m, "enter")
	out := m.View()
	if !strings.Contains(out, "w1") || !strings.Contains(out, "task-a") {
		t.Fatalf("focus view missing worker/task:\n%s", out)
	}
	if !strings.Contains(out, "build") {
		t.Fatalf("focus view missing current step:\n%s", out)
	}

	m, _ = press(t, m, "esc")
	out = m.View()
	if !strings.Contains(out, "NEED YOU") {
		t.Fatalf("esc did not return to fleet view:\n%s", out)
	}
}

type fakeResponder struct {
	runID, reqID, outcome string
	called                bool
}

func TestView_DecisionKeysInvokeRespondFn(t *testing.T) {
	cases := []struct {
		key     string
		outcome string
	}{
		{"a", "advance"},
		{"r", "retry"},
		{"x", "abort"},
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			fr := &fakeResponder{}
			m := NewModel(Options{
				RespondFn: func(runID, reqID, outcome string) tea.Cmd {
					fr.runID, fr.reqID, fr.outcome = runID, reqID, outcome
					fr.called = true
					return nil
				},
			})
			mm, _ := m.Update(StateUpdateMsg{RunID: "run-1", Kind: "step_start", WorkerID: "w1", Task: "task-a", Step: "build"})
			m = mm.(Model)
			mm, _ = m.Update(StateUpdateMsg{RunID: "run-1", Kind: "decision_request", WorkerID: "w1", RequestID: "req-1", Options: []string{"advance", "retry", "abort"}})
			m = mm.(Model)
			m, _ = press(t, m, "enter") // focus w1

			m, _ = press(t, m, tc.key)

			if !fr.called {
				t.Fatalf("RespondFn was not called for key %q", tc.key)
			}
			if fr.runID != "run-1" || fr.reqID != "req-1" || fr.outcome != tc.outcome {
				t.Fatalf("RespondFn called with (%q,%q,%q), want (run-1,req-1,%q)", fr.runID, fr.reqID, fr.outcome, tc.outcome)
			}
		})
	}
}

func TestView_AttachKeyWithNoPendingDecision(t *testing.T) {
	var attachedRunID string
	m := NewModel(Options{
		AttachFn: func(runID string) tea.Cmd {
			attachedRunID = runID
			return nil
		},
	})
	mm, _ := m.Update(StateUpdateMsg{RunID: "run-1", Kind: "step_start", WorkerID: "w1", Task: "task-a", Step: "build"})
	m = mm.(Model)
	m, _ = press(t, m, "enter") // focus w1, no pending decision

	m, _ = press(t, m, "a")

	if attachedRunID != "run-1" {
		t.Fatalf("AttachFn called with runID %q, want run-1", attachedRunID)
	}
}

func TestView_QuitKeys(t *testing.T) {
	m := NewModel(Options{})
	_, cmd := press(t, m, "q")
	if cmd == nil {
		t.Fatalf("'q' did not return a command")
	}
	_, cmd2 := press(t, m, "ctrl+c")
	if cmd2 == nil {
		t.Fatalf("'ctrl+c' did not return a command")
	}
}

func TestView_NKeyEntersNamingStage(t *testing.T) {
	m := twoWorkerModel()
	m, _ = press(t, m, "n")
	if m.view != viewNaming {
		t.Fatalf("view = %v, want viewNaming", m.view)
	}
	out := m.View()
	if !strings.Contains(out, "New loop") {
		t.Fatalf("naming view missing title:\n%s", out)
	}
	if !strings.Contains(out, "[enter] create") {
		t.Fatalf("naming view missing create hint:\n%s", out)
	}
}

func TestView_NamingEscCancelsBackToFleet(t *testing.T) {
	m := twoWorkerModel()
	m, _ = press(t, m, "n")
	m = typeRunes(t, m, "abandoned")
	m, _ = press(t, m, "esc")

	if m.view != viewFleet {
		t.Fatalf("view = %v, want viewFleet after esc", m.view)
	}
}

func TestView_NamingEmptyNameShowsError(t *testing.T) {
	dir := t.TempDir()
	m := NewModel(Options{ProjectDir: dir})
	m, _ = press(t, m, "n")
	m, _ = press(t, m, "enter")

	if m.view != viewNaming {
		t.Fatalf("view = %v, want to stay in viewNaming on empty name", m.view)
	}
	if !strings.Contains(m.View(), "loop name is required") {
		t.Fatalf("naming view missing empty-name error:\n%s", m.View())
	}
}

func TestView_NamingDuplicateNameShowsError(t *testing.T) {
	dir := t.TempDir()
	loopPath := filepath.Join(dir, ".looper", "loops", "dev-loop.yaml")
	if err := os.MkdirAll(filepath.Dir(loopPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loopPath, []byte("name: dev-loop\nsteps:\n  - name: a\n    type: manual\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewModel(Options{ProjectDir: dir})
	m, _ = press(t, m, "n")
	m = typeRunes(t, m, "dev-loop")
	m, _ = press(t, m, "enter")

	if m.view != viewNaming {
		t.Fatalf("view = %v, want to stay in viewNaming on duplicate name", m.view)
	}
	if !strings.Contains(m.View(), `"dev-loop"`) {
		t.Fatalf("naming view missing duplicate-name error:\n%s", m.View())
	}
}

// typeRunes feeds each character of s to m as individual key presses,
// simulating a user typing s into the naming stage's buffer.
func typeRunes(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = press(t, m, string(r))
	}
	return m
}

func TestView_EscCancelsBuilderWithoutSaving(t *testing.T) {
	called := false
	dir := t.TempDir()
	m := NewModel(Options{
		ProjectDir: dir,
		AuthorFn: func(req builder.AuthorRequest) tea.Cmd {
			called = true
			return func() tea.Msg { return builder.SessionDoneMsg{} }
		},
	})
	m, _ = press(t, m, "n")
	m = typeRunes(t, m, "dev-loop")
	m, _ = press(t, m, "enter")
	m, _ = press(t, m, "esc")

	if m.view != viewFleet {
		t.Fatalf("view = %v, want viewFleet after esc", m.view)
	}
	if called {
		t.Fatalf("AuthorFn was called despite cancelling with esc")
	}
}

func TestView_CtrlCQuitsFromBuilder(t *testing.T) {
	dir := t.TempDir()
	m := NewModel(Options{ProjectDir: dir})
	m, _ = press(t, m, "n")
	m = typeRunes(t, m, "dev-loop")
	m, _ = press(t, m, "enter")
	_, cmd := press(t, m, "ctrl+c")
	if cmd == nil {
		t.Fatalf("ctrl+c from builder view did not return a command")
	}
}

func TestView_CompletingBuilderSavesAndReturnsToFleet(t *testing.T) {
	dir := t.TempDir()
	loopPath := filepath.Join(dir, ".looper", "loops", "dev-loop.yaml")
	m := NewModel(Options{ProjectDir: dir})

	m, _ = press(t, m, "n")
	m = typeRunes(t, m, "dev-loop")
	m, _ = press(t, m, "enter") // enters builder; writes a skeleton loop to loopPath
	m, _ = press(t, m, "q")     // quit -> builder.Model.Quit() becomes true

	if m.view != viewFleet {
		t.Fatalf("view = %v, want viewFleet after quitting the builder", m.view)
	}
	want := "saved " + loopPath
	if !strings.Contains(m.View(), want) {
		t.Fatalf("fleet view missing save confirmation %q:\n%s", want, m.View())
	}
}

func TestView_FleetFooterMentionsNewLoopKey(t *testing.T) {
	m := twoWorkerModel()
	if !strings.Contains(m.View(), "[n] new loop") {
		t.Fatalf("fleet footer missing new-loop hint:\n%s", m.View())
	}
}
