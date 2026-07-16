package builder

import (
	"testing"

	"github.com/jbofill10/looper/config"

	tea "github.com/charmbracelet/bubbletea"
)

// typeString feeds each rune of s to m.Update as a synthetic tea.KeyMsg,
// simulating a user typing s into the current field.
func typeString(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		mm, ok := next.(Model)
		if !ok {
			t.Fatalf("Update did not return a builder.Model")
		}
		m = mm
	}
	return m
}

// pressEnter feeds a synthetic enter key to m.Update, committing the
// current field and advancing the stage.
func pressEnter(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update did not return a builder.Model")
	}
	return mm
}

// pressBackspace feeds a synthetic backspace key to m.Update.
func pressBackspace(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	mm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update did not return a builder.Model")
	}
	return mm
}

func TestBuilder_TwoStepLoop(t *testing.T) {
	m := New(nil)

	m = typeString(t, m, "dev-loop")
	m = pressEnter(t, m) // name

	m = pressEnter(t, m) // concurrency blank => 1

	m = typeString(t, m, "get-task")
	m = pressEnter(t, m) // step name

	m = typeString(t, m, "script")
	m = pressEnter(t, m) // step type

	m = typeString(t, m, "echo TASK_ID=1 >> $LOOPER_OUTPUT")
	m = pressEnter(t, m) // run

	m = typeString(t, m, "TASK_ID")
	m = pressEnter(t, m) // outputs

	m = pressEnter(t, m) // on_fail blank => ask

	m = typeString(t, m, "y")
	m = pressEnter(t, m) // add another

	m = typeString(t, m, "review")
	m = pressEnter(t, m) // step name

	m = typeString(t, m, "manual")
	m = pressEnter(t, m) // step type

	m = pressEnter(t, m) // outputs blank

	m = typeString(t, m, "n")
	m = pressEnter(t, m) // add another => done

	if !m.Done() {
		t.Fatalf("expected Done() true, stage=%v", m.stage)
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

func TestBuilder_InvalidStepTypeReprompts(t *testing.T) {
	m := New(nil)
	m = typeString(t, m, "loop")
	m = pressEnter(t, m) // name
	m = pressEnter(t, m) // concurrency

	m = typeString(t, m, "step-one")
	m = pressEnter(t, m) // step name

	if m.stage != stageStepType {
		t.Fatalf("stage = %v, want stageStepType", m.stage)
	}

	m = typeString(t, m, "bogus")
	m = pressEnter(t, m)

	if m.stage != stageStepType {
		t.Fatalf("after invalid type, stage = %v, want stageStepType (unchanged)", m.stage)
	}
	if m.input != "" {
		t.Errorf("input = %q, want cleared after invalid type", m.input)
	}

	m = typeString(t, m, "manual")
	m = pressEnter(t, m)

	if m.stage != stageStepOutputs {
		t.Fatalf("stage = %v, want stageStepOutputs after valid type", m.stage)
	}
}

func TestBuilder_ConcurrencyParsing(t *testing.T) {
	m := New(nil)
	m = typeString(t, m, "loop")
	m = pressEnter(t, m) // name

	m = typeString(t, m, "4")
	m = pressEnter(t, m) // concurrency

	if m.concurrency != 4 {
		t.Errorf("concurrency = %d, want 4", m.concurrency)
	}

	// blank case
	m2 := New(nil)
	m2 = typeString(t, m2, "loop2")
	m2 = pressEnter(t, m2)
	m2 = pressEnter(t, m2) // blank concurrency

	if m2.concurrency != 1 {
		t.Errorf("blank concurrency = %d, want 1", m2.concurrency)
	}
}

func TestBuilder_EditingPreservesNameAndConcurrency(t *testing.T) {
	existing := &config.Loop{
		Name:        "old-loop",
		Concurrency: 5,
		Steps: []config.Step{
			{Name: "existing-step", Type: config.StepManual},
		},
	}

	m := New(existing)
	m = pressEnter(t, m) // keep existing name
	m = pressEnter(t, m) // keep existing concurrency

	m2 := New(existing)
	m2 = pressEnter(t, m2)
	m2 = pressEnter(t, m2)
	m2 = typeString(t, m2, "step-two")
	m2 = pressEnter(t, m2)
	m2 = typeString(t, m2, "manual")
	m2 = pressEnter(t, m2)
	m2 = pressEnter(t, m2) // outputs blank
	m2 = typeString(t, m2, "n")
	m2 = pressEnter(t, m2)

	loop, ok := m2.Loop()
	if !ok {
		t.Fatalf("Loop() ok = false")
	}
	if loop.Name != "old-loop" {
		t.Errorf("name = %q, want old-loop (preserved)", loop.Name)
	}
	if loop.Concurrency != 5 {
		t.Errorf("concurrency = %d, want 5 (preserved)", loop.Concurrency)
	}
	if len(loop.Steps) != 2 {
		t.Fatalf("got %d steps, want 2 (1 existing + 1 new)", len(loop.Steps))
	}
	if loop.Steps[0].Name != "existing-step" {
		t.Errorf("step0 = %q, want existing-step preserved", loop.Steps[0].Name)
	}
	if err := loop.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestBuilder_Backspace(t *testing.T) {
	m := New(nil)
	m = typeString(t, m, "devv")
	m = pressBackspace(t, m)
	m = pressEnter(t, m) // name

	l := Model{}
	_ = l
	m = pressEnter(t, m) // concurrency blank
	m = typeString(t, m, "s")
	m = pressEnter(t, m) // step name
	m = typeString(t, m, "manual")
	m = pressEnter(t, m)
	m = pressEnter(t, m) // outputs
	m = typeString(t, m, "n")
	m = pressEnter(t, m)

	loop, ok := m.Loop()
	if !ok {
		t.Fatalf("Loop() ok = false")
	}
	if loop.Name != "dev" {
		t.Errorf("name = %q, want dev (after backspace)", loop.Name)
	}
}
