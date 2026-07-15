package runner

import (
	"path/filepath"
	"testing"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runctx"
)

func newRC(t *testing.T) *runctx.RunContext {
	t.Helper()
	rc, err := runctx.New(filepath.Join(t.TempDir(), "iter"))
	if err != nil {
		t.Fatalf("runctx.New: %v", err)
	}
	rc.Set("WORKDIR", t.TempDir())
	return rc
}

func TestScript_Success(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{Name: "ok", Type: config.StepScript, Run: "exit 0"}
	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
}

func TestScript_NoWork(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{Name: "gt", Type: config.StepScript, Run: "exit 78", SignalsNoWork: true}
	got, _ := exec.Run(rc, step)
	if got != OutcomeNoWork {
		t.Errorf("outcome = %v, want no-work", got)
	}
}

func TestScript_ExitNonZeroWithoutSignalIsFailure(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{Prompter: &FakePrompter{FailureOutcome: OutcomeAbort}}
	// 78 but NOT signals_no_work => treated as ordinary failure.
	step := config.Step{Name: "x", Type: config.StepScript, Run: "exit 78", OnFail: config.OnFailAsk}
	got, _ := exec.Run(rc, step)
	if got != OutcomeAbort {
		t.Errorf("outcome = %v, want abort (via prompter)", got)
	}
}

func TestScript_OnFailPolicies(t *testing.T) {
	rc := newRC(t)
	cases := []struct {
		policy config.OnFail
		want   Outcome
	}{
		{config.OnFailRetry, OutcomeRetry},
		{config.OnFailAbort, OutcomeAbort},
	}
	for _, c := range cases {
		exec := &ScriptExecutor{}
		step := config.Step{Name: "f", Type: config.StepScript, Run: "exit 1", OnFail: c.policy}
		got, err := exec.Run(rc, step)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got != c.want {
			t.Errorf("policy %q: outcome = %v, want %v", c.policy, got, c.want)
		}
	}
}

func TestScript_OnFailAskConsultsPrompter(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{Prompter: &FakePrompter{FailureOutcome: OutcomeRetry}}
	step := config.Step{Name: "f", Type: config.StepScript, Run: "exit 2", OnFail: config.OnFailAsk}
	got, _ := exec.Run(rc, step)
	if got != OutcomeRetry {
		t.Errorf("outcome = %v, want retry (from prompter)", got)
	}
}

func TestScript_CapturesDeclaredOutputs(t *testing.T) {
	rc := newRC(t)
	exec := &ScriptExecutor{}
	step := config.Step{
		Name:    "gt",
		Type:    config.StepScript,
		Run:     `printf 'TASK_ID=42\nIGNORED=zzz\n' >> "$LOOPER_OUTPUT"`,
		Outputs: []string{"TASK_ID"},
	}
	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if v, _ := rc.Get("TASK_ID"); v != "42" {
		t.Errorf("TASK_ID = %q, want 42", v)
	}
	if _, ok := rc.Get("IGNORED"); ok {
		t.Errorf("IGNORED should not be captured (not declared)")
	}
}

func TestScript_RunsInWorkdirWithContextEnv(t *testing.T) {
	rc := newRC(t)
	rc.Set("GREETING", "hi")
	exec := &ScriptExecutor{}
	step := config.Step{
		Name:    "env",
		Type:    config.StepScript,
		Run:     `echo "RESULT=$GREETING" >> "$LOOPER_OUTPUT"`,
		Outputs: []string{"RESULT"},
	}
	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if v, _ := rc.Get("RESULT"); v != "hi" {
		t.Errorf("RESULT = %q, want hi (context env not injected)", v)
	}
}
