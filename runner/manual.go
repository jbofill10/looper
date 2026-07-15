package runner

import (
	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runctx"
)

// ManualExecutor represents a human gate: it defers entirely to the Prompter.
type ManualExecutor struct {
	Prompter Prompter
}

func (e *ManualExecutor) Run(rc *runctx.RunContext, step config.Step) (Outcome, error) {
	return e.Prompter.Manual(step)
}
