package builder

import (
	"strings"
	"testing"

	"github.com/jbofill10/looper/config"

	tea "github.com/charmbracelet/bubbletea"
)

// press feeds a synthetic tea.KeyMsg to m.Update and returns the resulting
// Model.
func press(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	mm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update did not return a builder.Model")
	}
	return mm
}

func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// typeString feeds each rune of s as a synthetic tea.KeyMsg, simulating a
// user typing s into the focused text field.
func typeString(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// tabTo presses tab until m.focus reaches target, failing after a generous
// number of presses (well beyond the longest possible field list).
func tabTo(t *testing.T, m Model, target fieldID) Model {
	t.Helper()
	for i := 0; i < 20; i++ {
		if m.focus == target {
			return m
		}
		m = press(t, m, key(tea.KeyTab))
	}
	t.Fatalf("could not reach field %v; stuck at %v", target, m.focus)
	return m
}

// selectValue presses right until the focused select field's current
// option equals want, failing if it cycles all the way around without
// finding it.
func selectValue(t *testing.T, m Model, id fieldID, want string) Model {
	t.Helper()
	opts := m.selectOptions(id)
	for i := 0; i < len(opts)+1; i++ {
		if opts[m.selectIdx(id)] == want {
			return m
		}
		m = press(t, m, key(tea.KeyRight))
	}
	t.Fatalf("option %q not found among %v", want, opts)
	return m
}

func TestBuilder_TwoStepLoop(t *testing.T) {
	m := New(nil, nil, Options{})

	m = typeString(t, m, "dev-loop")
	m = tabTo(t, m, fStepName)

	m = typeString(t, m, "get-task")
	m = tabTo(t, m, fStepType)
	m = selectValue(t, m, fStepType, "script")

	m = tabTo(t, m, fStepRun)
	m = typeString(t, m, "echo TASK_ID=1 >> $LOOPER_OUTPUT")

	m = tabTo(t, m, fStepOutputs)
	m = typeString(t, m, "TASK_ID")

	m = tabTo(t, m, fAddStep)
	m = press(t, m, key(tea.KeyEnter))

	if len(m.steps) != 1 {
		t.Fatalf("got %d steps after add, want 1", len(m.steps))
	}
	if m.focus != fStepName {
		t.Errorf("focus after add = %v, want fStepName", m.focus)
	}

	m = typeString(t, m, "review")
	m = tabTo(t, m, fStepType)
	m = selectValue(t, m, fStepType, "manual")

	m = tabTo(t, m, fAddStep)
	m = press(t, m, key(tea.KeyEnter))

	if len(m.steps) != 2 {
		t.Fatalf("got %d steps after second add, want 2", len(m.steps))
	}

	m = tabTo(t, m, fFinish)
	m = press(t, m, key(tea.KeyEnter))

	if !m.Done() {
		t.Fatalf("expected Done() true, errMsg=%q", m.errMsg)
	}

	l, ok := m.Loop()
	if !ok {
		t.Fatalf("Loop() ok = false, want true")
	}
	if l.Name != "dev-loop" {
		t.Errorf("name = %q, want dev-loop", l.Name)
	}
	if l.Concurrency != 1 {
		t.Errorf("concurrency = %d, want 1", l.Concurrency)
	}
	if len(l.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(l.Steps))
	}
	s0 := l.Steps[0]
	if s0.Name != "get-task" || s0.Type != config.StepScript {
		t.Errorf("step0 = %+v, want name get-task type script", s0)
	}
	if s0.Run != "echo TASK_ID=1 >> $LOOPER_OUTPUT" {
		t.Errorf("step0.Run = %q", s0.Run)
	}
	if len(s0.Outputs) != 1 || s0.Outputs[0] != "TASK_ID" {
		t.Errorf("step0.Outputs = %v, want [TASK_ID]", s0.Outputs)
	}
	if s0.OnFail != config.OnFailAsk {
		t.Errorf("step0.OnFail = %q, want ask", s0.OnFail)
	}
	s1 := l.Steps[1]
	if s1.Name != "review" || s1.Type != config.StepManual {
		t.Errorf("step1 = %+v, want name review type manual", s1)
	}

	if err := l.Validate(); err != nil {
		t.Errorf("produced loop failed Validate: %v", err)
	}
}

func TestBuilder_SelectFieldsCycleAndWrap(t *testing.T) {
	m := New(nil, []string{"gemini"}, Options{})
	m = tabTo(t, m, fStepType)

	if got := m.selectOptions(fStepType)[m.selectIdx(fStepType)]; got != "script" {
		t.Fatalf("initial step type = %q, want script (index 0)", got)
	}

	m = press(t, m, key(tea.KeyRight))
	if got := m.selectOptions(fStepType)[m.selectIdx(fStepType)]; got != "headless" {
		t.Errorf("after right, step type = %q, want headless", got)
	}

	m = press(t, m, key(tea.KeyLeft))
	if got := m.selectOptions(fStepType)[m.selectIdx(fStepType)]; got != "script" {
		t.Errorf("after left, step type = %q, want script", got)
	}

	// left from the first option wraps to the last.
	m = press(t, m, key(tea.KeyLeft))
	if got := m.selectOptions(fStepType)[m.selectIdx(fStepType)]; got != "manual" {
		t.Errorf("after wrap-left, step type = %q, want manual", got)
	}
}

func TestBuilder_HarnessSelectIncludesConfiguredNames(t *testing.T) {
	m := New(nil, []string{"gemini", "claude"}, Options{})
	if got := m.harnessOptions; len(got) != 3 || got[0] != "(default)" {
		t.Fatalf("harnessOptions = %v, want [(default) claude gemini]", got)
	}
	if m.harnessOptions[1] != "claude" || m.harnessOptions[2] != "gemini" {
		t.Errorf("harnessOptions not sorted: %v", m.harnessOptions)
	}
}

func TestBuilder_HeadlessStepUsesSelectedHarness(t *testing.T) {
	m := New(nil, []string{"gemini"}, Options{})
	m = typeString(t, m, "loop")
	m = tabTo(t, m, fStepName)
	m = typeString(t, m, "work")

	m = tabTo(t, m, fStepType)
	m = selectValue(t, m, fStepType, "headless")

	m = tabTo(t, m, fStepPrompt)
	m = typeString(t, m, "do the thing")

	m = tabTo(t, m, fStepHarness)
	m = selectValue(t, m, fStepHarness, "gemini")

	m = tabTo(t, m, fAddStep)
	m = press(t, m, key(tea.KeyEnter))

	if len(m.steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(m.steps))
	}
	s := m.steps[0]
	if s.Harness != "gemini" {
		t.Errorf("step.Harness = %q, want gemini", s.Harness)
	}
	if s.Prompt != "do the thing" {
		t.Errorf("step.Prompt = %q", s.Prompt)
	}

	m = tabTo(t, m, fFinish)
	m = press(t, m, key(tea.KeyEnter))
	if !m.Done() {
		t.Fatalf("expected Done() true, errMsg=%q", m.errMsg)
	}
}

func TestBuilder_AddStepRejectsDuplicateName(t *testing.T) {
	m := New(nil, nil, Options{})
	m = tabTo(t, m, fStepName)
	m = typeString(t, m, "same")
	m = tabTo(t, m, fStepType)
	m = selectValue(t, m, fStepType, "manual")
	m = tabTo(t, m, fAddStep)
	m = press(t, m, key(tea.KeyEnter))
	if len(m.steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(m.steps))
	}

	m = typeString(t, m, "same")
	m = tabTo(t, m, fStepType)
	m = selectValue(t, m, fStepType, "manual")
	m = tabTo(t, m, fAddStep)
	m = press(t, m, key(tea.KeyEnter))

	if len(m.steps) != 1 {
		t.Errorf("got %d steps after duplicate add, want still 1", len(m.steps))
	}
	if m.errMsg == "" {
		t.Errorf("expected errMsg for duplicate step name")
	}
	if m.focus != fStepName {
		t.Errorf("focus = %v, want fStepName after duplicate error", m.focus)
	}
}

func TestBuilder_ScriptStepRequiresRun(t *testing.T) {
	m := New(nil, nil, Options{})
	m = tabTo(t, m, fStepName)
	m = typeString(t, m, "build")
	// step type defaults to script; leave Run blank.
	m = tabTo(t, m, fAddStep)
	m = press(t, m, key(tea.KeyEnter))

	if len(m.steps) != 0 {
		t.Fatalf("got %d steps, want 0 (blank run should be rejected)", len(m.steps))
	}
	if m.errMsg == "" {
		t.Errorf("expected errMsg for missing run command")
	}
	if m.focus != fStepRun {
		t.Errorf("focus = %v, want fStepRun", m.focus)
	}
}

func TestBuilder_FinishRequiresLoopNameAndAtLeastOneStep(t *testing.T) {
	m := New(nil, nil, Options{})
	m = tabTo(t, m, fFinish)
	m = press(t, m, key(tea.KeyEnter))

	if m.Done() {
		t.Fatalf("expected Done() false with no name and no steps")
	}
	if m.errMsg == "" {
		t.Errorf("expected errMsg")
	}
}

func TestBuilder_ConcurrencyParsing(t *testing.T) {
	m := New(nil, nil, Options{})
	m = typeString(t, m, "loop")
	m = tabTo(t, m, fConcurrency)
	m = typeString(t, m, "4")
	m = tabTo(t, m, fStepName)
	m = typeString(t, m, "s")
	m = tabTo(t, m, fStepType)
	m = selectValue(t, m, fStepType, "manual")
	m = tabTo(t, m, fAddStep)
	m = press(t, m, key(tea.KeyEnter))
	m = tabTo(t, m, fFinish)
	m = press(t, m, key(tea.KeyEnter))

	if !m.Done() {
		t.Fatalf("expected Done() true, errMsg=%q", m.errMsg)
	}
	loop, _ := m.Loop()
	if loop.Concurrency != 4 {
		t.Errorf("concurrency = %d, want 4", loop.Concurrency)
	}
}

func TestBuilder_ConcurrencyInvalidBlocksFinish(t *testing.T) {
	m := New(nil, nil, Options{})
	m = typeString(t, m, "loop")
	m = tabTo(t, m, fConcurrency)
	m = typeString(t, m, "nope")
	m = tabTo(t, m, fStepName)
	m = typeString(t, m, "s")
	m = tabTo(t, m, fStepType)
	m = selectValue(t, m, fStepType, "manual")
	m = tabTo(t, m, fAddStep)
	m = press(t, m, key(tea.KeyEnter))
	m = tabTo(t, m, fFinish)
	m = press(t, m, key(tea.KeyEnter))

	if m.Done() {
		t.Fatalf("expected Done() false with invalid concurrency")
	}
	if m.focus != fConcurrency {
		t.Errorf("focus = %v, want fConcurrency", m.focus)
	}
}

func TestBuilder_EditingPreservesNameConcurrencyAndSteps(t *testing.T) {
	existing := &config.Loop{
		Name:        "old-loop",
		Concurrency: 5,
		Steps: []config.Step{
			{Name: "existing-step", Type: config.StepManual},
		},
	}

	m := New(existing, nil, Options{})
	if m.name != "old-loop" {
		t.Errorf("name = %q, want old-loop preloaded", m.name)
	}
	if m.concurrency != "5" {
		t.Errorf("concurrency buffer = %q, want 5 preloaded", m.concurrency)
	}
	if len(m.steps) != 1 || m.steps[0].Name != "existing-step" {
		t.Fatalf("steps = %+v, want [existing-step] preloaded", m.steps)
	}

	m = tabTo(t, m, fStepName)
	m = typeString(t, m, "step-two")
	m = tabTo(t, m, fStepType)
	m = selectValue(t, m, fStepType, "manual")
	m = tabTo(t, m, fAddStep)
	m = press(t, m, key(tea.KeyEnter))
	m = tabTo(t, m, fFinish)
	m = press(t, m, key(tea.KeyEnter))

	loop, ok := m.Loop()
	if !ok {
		t.Fatalf("Loop() ok = false")
	}
	if loop.Name != "old-loop" || loop.Concurrency != 5 {
		t.Errorf("loop = %+v, want name old-loop concurrency 5 preserved", loop)
	}
	if len(loop.Steps) != 2 || loop.Steps[0].Name != "existing-step" || loop.Steps[1].Name != "step-two" {
		t.Fatalf("steps = %+v, want [existing-step step-two]", loop.Steps)
	}
}

func TestBuilder_Backspace(t *testing.T) {
	m := New(nil, nil, Options{})
	m = typeString(t, m, "devv")
	m = press(t, m, key(tea.KeyBackspace))
	if m.name != "dev" {
		t.Fatalf("name = %q, want dev after backspace", m.name)
	}
}

func TestBuilder_DraftRequiresScriptStepAndInvokesDraftFn(t *testing.T) {
	var got DraftRequest
	called := false
	opts := Options{
		DraftFn: func(req DraftRequest) tea.Cmd {
			called = true
			got = req
			return func() tea.Msg { return DraftedMsg{Content: "echo hi\n"} }
		},
	}

	m := New(nil, nil, opts)
	m = typeString(t, m, "loop")
	m = tabTo(t, m, fStepName)
	m = typeString(t, m, "build")
	// step type already defaults to script.

	m = press(t, m, key(tea.KeyCtrlD))
	if !called {
		t.Fatalf("DraftFn was not invoked for a script step")
	}
	if got.LoopName != "loop" || got.StepName != "build" {
		t.Errorf("DraftRequest = %+v, want LoopName=loop StepName=build", got)
	}
	if !m.drafting {
		t.Errorf("expected drafting=true after requesting a draft session")
	}

	next, _ := m.Update(DraftedMsg{Content: "echo hi\n"})
	m = next.(Model)
	if m.drafting {
		t.Errorf("expected drafting=false after DraftedMsg")
	}
	if m.curRun != "echo hi" {
		t.Errorf("curRun = %q, want %q (trimmed)", m.curRun, "echo hi")
	}
}

func TestBuilder_DraftIgnoredForNonScriptStep(t *testing.T) {
	called := false
	opts := Options{DraftFn: func(DraftRequest) tea.Cmd {
		called = true
		return nil
	}}

	m := New(nil, nil, opts)
	m = tabTo(t, m, fStepType)
	m = selectValue(t, m, fStepType, "manual")
	m = press(t, m, key(tea.KeyCtrlD))

	if called {
		t.Errorf("DraftFn should not be invoked for a manual step")
	}
}

func TestBuilder_DraftedMsgErrorSetsErrMsg(t *testing.T) {
	m := New(nil, nil, Options{})
	next, _ := m.Update(DraftedMsg{Err: errString("boom")})
	m = next.(Model)
	if m.drafting {
		t.Errorf("expected drafting=false after an errored DraftedMsg")
	}
	if !strings.Contains(m.errMsg, "boom") {
		t.Errorf("errMsg = %q, want it to mention the underlying error", m.errMsg)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
