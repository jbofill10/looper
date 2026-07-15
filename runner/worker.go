package runner

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runctx"
)

// Worker drives one loop's iterations single-threaded, in-process.
type Worker struct {
	Loop     *config.Loop
	BaseDir  string // the .looper dir
	Workdir  string // execution dir (workspace: shared)
	Prompter Prompter
	NewID    func() string
}

func (w *Worker) idGen() func() string {
	if w.NewID != nil {
		return w.NewID
	}
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("%s-%03d", time.Now().UTC().Format("20060102T150405"), n)
	}
}

// Run executes iterations until get-task signals no-work, max_iterations is
// reached, or a step aborts the loop.
func (w *Worker) Run() error {
	gen := w.idGen()
	for iter := 1; w.Loop.MaxIterations == 0 || iter <= w.Loop.MaxIterations; iter++ {
		id := gen()
		stop, err := w.runIteration(id)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// runIteration runs all steps for one work unit. It returns stop=true when the
// loop should end (no-work signalled).
func (w *Worker) runIteration(id string) (stop bool, err error) {
	dir := filepath.Join(w.BaseDir, "runs", w.Loop.Name, id)
	rc, err := runctx.New(dir)
	if err != nil {
		return false, err
	}
	rc.Set("WORKDIR", w.Workdir)

	var digest strings.Builder
	fmt.Fprintf(&digest, "# Iteration %s\n\n", id)

	i := 0
	for i < len(w.Loop.Steps) {
		step := w.Loop.Steps[i]
		exec, err := w.executorFor(step)
		if err != nil {
			return false, err
		}
		_ = rc.AppendEvent(runctx.Event{Step: step.Name, Kind: "start"})
		outcome, err := exec.Run(rc, step)
		if err != nil {
			return false, fmt.Errorf("step %q: %w", step.Name, err)
		}
		_ = rc.AppendEvent(runctx.Event{Step: step.Name, Kind: "outcome", Message: outcome.String()})
		fmt.Fprintf(&digest, "- %s → %s\n", step.Name, outcome)

		if err := rc.Save(); err != nil {
			return false, err
		}

		switch outcome {
		case OutcomeAdvance:
			i++
		case OutcomeRetry:
			// stay on the same step
		case OutcomeAbort:
			_ = rc.WriteDigest(digest.String())
			return false, nil
		case OutcomeNoWork:
			_ = rc.WriteDigest(digest.String())
			return true, nil
		}
	}
	return false, rc.WriteDigest(digest.String())
}

func (w *Worker) executorFor(step config.Step) (Executor, error) {
	switch step.Type {
	case config.StepScript:
		return &ScriptExecutor{Prompter: w.Prompter}, nil
	case config.StepManual:
		return &ManualExecutor{Prompter: w.Prompter}, nil
	case config.StepInteractive, config.StepHeadless:
		return nil, fmt.Errorf("step %q: type %q not supported until a later milestone", step.Name, step.Type)
	default:
		return nil, fmt.Errorf("step %q: unknown type %q", step.Name, step.Type)
	}
}
