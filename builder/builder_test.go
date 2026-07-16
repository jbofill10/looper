package builder

import (
	"os"
	"strings"
	"testing"

	"github.com/jbofill10/looper/config"

	tea "github.com/charmbracelet/bubbletea"
)

func press(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	mm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update did not return a builder.Model")
	}
	return mm
}

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func writeLoop(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dirOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return "."
	}
	return path[:i]
}

func TestNew_CreatesSkeletonWhenLoopFileMissing(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/fresh.yaml"

	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(m.Steps()) != 0 {
		t.Errorf("got %d steps, want 0 for a fresh skeleton", len(m.Steps()))
	}
	if _, err := os.Stat(loopPath); err != nil {
		t.Errorf("expected skeleton written at %s: %v", loopPath, err)
	}
}

func TestNew_LoadsExistingLoop(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: manual\n")

	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(m.Steps()) != 1 || m.Steps()[0].Name != "a" {
		t.Errorf("Steps() = %+v, want one step named a", m.Steps())
	}
}

func TestCreateStep_InvokesAuthorFnWithBlankStepName(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/fresh.yaml"
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	var gotReq AuthorRequest
	m.opts.AuthorFn = func(req AuthorRequest) tea.Cmd {
		gotReq = req
		return func() tea.Msg { return SessionDoneMsg{} }
	}

	m = press(t, m, key("c"))
	if gotReq.StepName != "" {
		t.Errorf("StepName = %q, want empty for create-step", gotReq.StepName)
	}
	if gotReq.LoopPath != loopPath {
		t.Errorf("LoopPath = %q, want %q", gotReq.LoopPath, loopPath)
	}
}

func TestEditStep_InvokesAuthorFnWithSelectedStepAndNoError(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: manual\n")
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	var gotReq AuthorRequest
	m.opts.AuthorFn = func(req AuthorRequest) tea.Cmd {
		gotReq = req
		return func() tea.Msg { return SessionDoneMsg{} }
	}

	m = press(t, m, key("e"))
	if gotReq.StepName != "a" {
		t.Errorf("StepName = %q, want a", gotReq.StepName)
	}
	if gotReq.ValidationErr != nil {
		t.Errorf("ValidationErr = %v, want nil for a valid step", gotReq.ValidationErr)
	}
}

func TestSessionDoneMsg_ReloadsAndFlagsInvalidStep(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: manual\n")
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the harness session having rewritten the file with an
	// invalid step before signaling done.
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: interactive\n")
	m = press(t, m, SessionDoneMsg{})

	errs := m.StepErrors()
	if errs["a"] == nil {
		t.Errorf("expected step %q to be flagged invalid after reload", "a")
	}
}

func TestRevalidate_FlagsDuplicateStepNamesOnBothSteps(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: manual\n  - name: a\n    type: manual\n")
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	errs := m.StepErrors()
	if len(m.Steps()) != 2 {
		t.Fatalf("expected 2 steps loaded, got %d", len(m.Steps()))
	}
	if errs["a"] == nil {
		t.Errorf("expected duplicate step name %q to be flagged invalid", "a")
	}
}

func TestEditStep_OnInvalidStepIncludesErrorInRequest(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: interactive\n")
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	var gotReq AuthorRequest
	m.opts.AuthorFn = func(req AuthorRequest) tea.Cmd {
		gotReq = req
		return func() tea.Msg { return SessionDoneMsg{} }
	}

	m = press(t, m, key("e"))
	if gotReq.ValidationErr == nil {
		t.Fatal("expected ValidationErr to be set for an invalid step")
	}
}

func TestDeleteStep_RewritesFileWithoutSession(t *testing.T) {
	dir := t.TempDir()
	loopPath := dir + "/.looper/loops/existing.yaml"
	writeLoop(t, loopPath, "name: existing\nsteps:\n  - name: a\n    type: manual\n  - name: b\n    type: manual\n")
	m, err := New(dir, loopPath, Options{})
	if err != nil {
		t.Fatal(err)
	}

	m = press(t, m, key("d")) // deletes the selected (first) step, "a"

	loop, err := config.LoadLoop(loopPath)
	if err != nil {
		t.Fatalf("LoadLoop after delete: %v", err)
	}
	if len(loop.Steps) != 1 || loop.Steps[0].Name != "b" {
		t.Errorf("steps after delete = %+v, want only step b", loop.Steps)
	}
}

func TestQuit_SetOnQKey(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, dir+"/.looper/loops/x.yaml", Options{})
	if err != nil {
		t.Fatal(err)
	}
	m = press(t, m, key("q"))
	if !m.Quit() {
		t.Error("Quit() = false after pressing q, want true")
	}
}
