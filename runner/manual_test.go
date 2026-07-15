package runner

import (
	"strings"
	"testing"

	"github.com/jbofill10/looper/config"
)

func TestManual_DelegatesToPrompter(t *testing.T) {
	rc := newRC(t)
	fp := &FakePrompter{ManualOutcome: OutcomeAdvance}
	exec := &ManualExecutor{Prompter: fp}
	got, err := exec.Run(rc, config.Step{Name: "review", Type: config.StepManual})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
	if fp.ManualCalls != 1 {
		t.Errorf("ManualCalls = %d, want 1", fp.ManualCalls)
	}
}

func TestStdinPrompter_Manual(t *testing.T) {
	cases := map[string]Outcome{
		"a\n": OutcomeAdvance,
		"r\n": OutcomeRetry,
		"x\n": OutcomeAbort,
	}
	for input, want := range cases {
		p := &StdinPrompter{In: strings.NewReader(input), Out: &strings.Builder{}}
		got, err := p.Manual(config.Step{Name: "s", Type: config.StepManual})
		if err != nil {
			t.Fatalf("Manual(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("Manual(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestStdinPrompter_AskFailure(t *testing.T) {
	p := &StdinPrompter{In: strings.NewReader("r\n"), Out: &strings.Builder{}}
	got, err := p.AskFailure(config.Step{Name: "s"}, 1)
	if err != nil {
		t.Fatalf("AskFailure: %v", err)
	}
	if got != OutcomeRetry {
		t.Errorf("AskFailure = %v, want retry", got)
	}
}
