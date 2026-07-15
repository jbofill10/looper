package runner

import (
	"testing"

	"github.com/jbofill10/looper/config"
)

func TestHeadless_Success(t *testing.T) {
	rc := newRC(t)
	h := config.Harness{Headless: []string{"sh", "-c", "echo done"}}
	exec := &HeadlessExecutor{Harness: h}
	step := config.Step{Name: "hl", Type: config.StepHeadless, Prompt: "hi"}
	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
}

func TestHeadless_InterpolationAndOutputs(t *testing.T) {
	rc := newRC(t)
	h := config.Harness{Headless: []string{"sh", "-c", "{{PROMPT}}"}}
	exec := &HeadlessExecutor{Harness: h}
	step := config.Step{
		Name:    "hl",
		Type:    config.StepHeadless,
		Prompt:  `echo TASK_ID=7 >> "$LOOPER_OUTPUT"`,
		Outputs: []string{"TASK_ID"},
	}
	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
	v, ok := rc.Get("TASK_ID")
	if !ok || v != "7" {
		t.Errorf("TASK_ID = %q, ok=%v, want 7", v, ok)
	}
}

func TestHeadless_SentinelVarAvailable(t *testing.T) {
	rc := newRC(t)
	def := config.DefaultGlobal()
	claude, err := def.ResolveHarness("claude")
	if err != nil {
		t.Fatalf("ResolveHarness: %v", err)
	}
	claude.Headless = []string{"sh", "-c", "{{PROMPT}}"}
	exec := &HeadlessExecutor{Harness: claude}
	step := config.Step{
		Name:    "hl",
		Type:    config.StepHeadless,
		Prompt:  `echo NEEDS={{SENTINEL_DONE}} >> "$LOOPER_OUTPUT"`,
		Outputs: []string{"NEEDS"},
	}
	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
	v, ok := rc.Get("NEEDS")
	if !ok || v != claude.Sentinels.Done {
		t.Errorf("NEEDS = %q, ok=%v, want %q", v, ok, claude.Sentinels.Done)
	}
}

func TestHeadless_FailureOnFailAbort(t *testing.T) {
	rc := newRC(t)
	h := config.Harness{Headless: []string{"sh", "-c", "exit 1"}}
	exec := &HeadlessExecutor{Harness: h}
	step := config.Step{Name: "hl", Type: config.StepHeadless, Prompt: "hi", OnFail: config.OnFailAbort}
	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAbort {
		t.Errorf("outcome = %v, want abort", got)
	}
}

func TestHeadless_NoWork(t *testing.T) {
	rc := newRC(t)
	h := config.Harness{Headless: []string{"sh", "-c", "exit 78"}}
	exec := &HeadlessExecutor{Harness: h}
	step := config.Step{Name: "hl", Type: config.StepHeadless, Prompt: "hi", SignalsNoWork: true}
	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeNoWork {
		t.Errorf("outcome = %v, want no-work", got)
	}
}
