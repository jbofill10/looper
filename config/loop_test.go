package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "loop.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func TestLoadLoop_Valid(t *testing.T) {
	p := writeTemp(t, `
name: dev-loop
concurrency: 1
max_iterations: 0
steps:
  - name: get-task
    type: script
    run: "echo TASK_ID=1 >> $LOOPER_OUTPUT"
    outputs: [TASK_ID]
    signals_no_work: true
  - name: review
    type: manual
`)
	loop, err := LoadLoop(p)
	if err != nil {
		t.Fatalf("LoadLoop: %v", err)
	}
	if loop.Name != "dev-loop" {
		t.Errorf("name = %q, want dev-loop", loop.Name)
	}
	if len(loop.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(loop.Steps))
	}
	if loop.Steps[0].Type != StepScript {
		t.Errorf("step0 type = %q, want script", loop.Steps[0].Type)
	}
	if !loop.Steps[0].SignalsNoWork {
		t.Errorf("step0 signals_no_work = false, want true")
	}
	if loop.Steps[1].Type != StepManual {
		t.Errorf("step1 type = %q, want manual", loop.Steps[1].Type)
	}
}

func TestLoadLoop_InvalidCases(t *testing.T) {
	cases := map[string]string{
		"no name":            "steps:\n  - name: a\n    type: manual\n",
		"no steps":           "name: x\nsteps: []\n",
		"unknown type":       "name: x\nsteps:\n  - name: a\n    type: bogus\n",
		"script missing run": "name: x\nsteps:\n  - name: a\n    type: script\n",
		"dup step names":     "name: x\nsteps:\n  - name: a\n    type: manual\n  - name: a\n    type: manual\n",
		"step missing name":  "name: x\nsteps:\n  - type: manual\n",
		"bad on_fail":        "name: x\nsteps:\n  - name: a\n    type: script\n    run: \"true\"\n    on_fail: explode\n",
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			if _, err := LoadLoop(writeTemp(t, body)); err == nil {
				t.Fatalf("expected error for %q, got nil", label)
			}
		})
	}
}

func TestValidate_DefaultsConcurrency(t *testing.T) {
	l := &Loop{Name: "x", Steps: []Step{{Name: "a", Type: StepManual}}}
	if err := l.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if l.Concurrency != 1 {
		t.Errorf("concurrency = %d, want default 1", l.Concurrency)
	}
}

func TestValidate_DefaultsTaskVar(t *testing.T) {
	l := &Loop{Name: "x", Steps: []Step{{Name: "a", Type: StepManual}}}
	if err := l.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if l.TaskVar != "TASK_ID" {
		t.Errorf("task_var = %q, want default TASK_ID", l.TaskVar)
	}
}

func TestValidate_PreservesExplicitTaskVar(t *testing.T) {
	l := &Loop{Name: "x", TaskVar: "ISSUE_ID", Steps: []Step{{Name: "a", Type: StepManual}}}
	if err := l.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if l.TaskVar != "ISSUE_ID" {
		t.Errorf("task_var = %q, want ISSUE_ID (unchanged)", l.TaskVar)
	}
}

func TestLoadLoop_TaskVarFromYAML(t *testing.T) {
	p := writeTemp(t, `
name: dev-loop
task_var: ISSUE_ID
steps:
  - name: a
    type: manual
`)
	loop, err := LoadLoop(p)
	if err != nil {
		t.Fatalf("LoadLoop: %v", err)
	}
	if loop.TaskVar != "ISSUE_ID" {
		t.Errorf("task_var = %q, want ISSUE_ID", loop.TaskVar)
	}
}

func TestStepValidate_Valid(t *testing.T) {
	cases := []Step{
		{Name: "a", Type: StepManual},
		{Name: "a", Type: StepScript, Run: "true"},
		{Name: "a", Type: StepHeadless, Prompt: "go"},
		{Name: "a", Type: StepInteractive, Prompt: "go"},
		{Name: "a", Type: StepScript, Run: "true", Outputs: []string{"X"}, Digest: "Y"},
		{Name: "a", Type: StepHeadless, Prompt: "go", Digest: "DIGEST_FILE"},
	}
	for _, s := range cases {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", s, err)
		}
	}
}

func TestStepValidate_DefaultsOnFail(t *testing.T) {
	s := Step{Name: "a", Type: StepScript, Run: "true"}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.OnFail != OnFailAsk {
		t.Errorf("on_fail = %q, want default %q", s.OnFail, OnFailAsk)
	}
}

func TestStepValidate_InvalidCases(t *testing.T) {
	cases := map[string]Step{
		"no name":                    {Type: StepManual},
		"unknown type":               {Name: "a", Type: "bogus"},
		"script missing run":         {Name: "a", Type: StepScript},
		"headless missing prompt":    {Name: "a", Type: StepHeadless},
		"interactive missing prompt": {Name: "a", Type: StepInteractive},
		"bad on_fail":                {Name: "a", Type: StepScript, Run: "true", OnFail: "explode"},
		"digest duplicates outputs":  {Name: "a", Type: StepScript, Run: "true", Outputs: []string{"D"}, Digest: "D"},
		"digest on manual step":      {Name: "a", Type: StepManual, Digest: "D"},
	}
	for label, s := range cases {
		t.Run(label, func(t *testing.T) {
			if err := s.Validate(); err == nil {
				t.Fatalf("expected error for %q, got nil", label)
			}
		})
	}
}

func TestLoadLoopLenient_AllowsZeroStepsAndMissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "l.yaml")
	if err := os.WriteFile(path, []byte("name: l\nsteps: []\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	loop, err := LoadLoopLenient(path)
	if err != nil {
		t.Fatalf("LoadLoopLenient: %v", err)
	}
	if loop.Name != "l" || len(loop.Steps) != 0 {
		t.Errorf("loop = %+v, want Name=l Steps=[]", loop)
	}
}

func TestLoadLoopLenient_MissingFileReturnsNotExist(t *testing.T) {
	_, err := LoadLoopLenient(filepath.Join(t.TempDir(), "missing.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want wrapping os.ErrNotExist", err)
	}
}
