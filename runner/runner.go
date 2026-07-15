// Package runner executes a loop's steps and drives the worker iteration loop.
package runner

import (
	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runctx"
)

// NoWorkExitCode is the reserved script exit code meaning "no work available".
const NoWorkExitCode = 78

// Outcome is the result of running a step, deciding what the worker does next.
type Outcome int

const (
	OutcomeAdvance Outcome = iota // move to the next step
	OutcomeRetry                  // re-run the current step
	OutcomeAbort                  // stop this iteration
	OutcomeNoWork                 // no work available; stop the loop
)

func (o Outcome) String() string {
	switch o {
	case OutcomeAdvance:
		return "advance"
	case OutcomeRetry:
		return "retry"
	case OutcomeAbort:
		return "abort"
	case OutcomeNoWork:
		return "no-work"
	default:
		return "unknown"
	}
}

// Executor runs a single step and reports its outcome.
type Executor interface {
	Run(rc *runctx.RunContext, step config.Step) (Outcome, error)
}

// Prompter handles human interaction: manual steps, on_fail=ask decisions,
// and confirming the outcome of an interactive session.
type Prompter interface {
	AskFailure(step config.Step, exitCode int) (Outcome, error)
	Manual(step config.Step) (Outcome, error)
	// Interactive asks the human to confirm the outcome of an interactive
	// session that has ended in finalState.
	Interactive(step config.Step, finalState string) (Outcome, error)
}

// FakePrompter is a test double returning preset outcomes.
type FakePrompter struct {
	FailureOutcome       Outcome
	ManualOutcome        Outcome
	InteractiveOutcome   Outcome
	FailureCalls         int
	ManualCalls          int
	InteractiveCalls     int
	LastInteractiveState string
}

func (f *FakePrompter) AskFailure(step config.Step, exitCode int) (Outcome, error) {
	f.FailureCalls++
	return f.FailureOutcome, nil
}

func (f *FakePrompter) Manual(step config.Step) (Outcome, error) {
	f.ManualCalls++
	return f.ManualOutcome, nil
}

func (f *FakePrompter) Interactive(step config.Step, finalState string) (Outcome, error) {
	f.InteractiveCalls++
	f.LastInteractiveState = finalState
	return f.InteractiveOutcome, nil
}
