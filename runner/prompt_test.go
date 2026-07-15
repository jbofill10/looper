package runner

import (
	"strings"
	"testing"

	"github.com/jbofill10/looper/config"
)

func TestStdinPrompter_Interactive(t *testing.T) {
	cases := map[string]Outcome{
		"a\n": OutcomeAdvance,
		"r\n": OutcomeRetry,
		"x\n": OutcomeAbort,
	}
	for input, want := range cases {
		p := &StdinPrompter{In: strings.NewReader(input), Out: &strings.Builder{}}
		got, err := p.Interactive(config.Step{Name: "s", Type: config.StepInteractive}, "awaiting_approval")
		if err != nil {
			t.Fatalf("Interactive(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("Interactive(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestFakePrompter_Interactive(t *testing.T) {
	fp := &FakePrompter{InteractiveOutcome: OutcomeAdvance}
	got, err := fp.Interactive(config.Step{Name: "s"}, "awaiting_approval")
	if err != nil {
		t.Fatalf("Interactive: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
	if fp.InteractiveCalls != 1 {
		t.Errorf("InteractiveCalls = %d, want 1", fp.InteractiveCalls)
	}
	if fp.LastInteractiveState != "awaiting_approval" {
		t.Errorf("LastInteractiveState = %q, want %q", fp.LastInteractiveState, "awaiting_approval")
	}
}
