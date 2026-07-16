package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runctx"
)

// Report kinds emitted via Worker.OnReport.
const (
	ReportIteration = "iteration"
	ReportStepStart = "step_start"
	ReportOutcome   = "outcome"
	ReportRunDone   = "run_done"
)

// Report is a single progress event emitted by a Worker while running a
// loop, delivered to Worker.OnReport if set.
type Report struct {
	Kind      string
	Step      string
	State     string
	Message   string
	Iteration int
}

// Worker drives one loop's iterations single-threaded, in-process.
type Worker struct {
	Loop        *config.Loop
	BaseDir     string // the .looper dir
	Workdir     string // execution dir (workspace: shared)
	Prompter    Prompter
	NewID       func() string
	Global      *config.Global // harness configuration; defaults to config.DefaultGlobal()
	HarnessName string         // default harness name; falls back to Global.DefaultHarness
	LooperBin   string         // absolute path to the looper binary, used by interactive steps for hook wiring

	// OnReport, if non-nil, is called synchronously as the worker makes
	// progress: at the start of each iteration, at each step start, after
	// each step outcome, and once when the run loop ends.
	OnReport func(Report)

	// Ctx, if non-nil, is checked before each step and between iterations;
	// once cancelled, Run returns Ctx.Err() promptly.
	Ctx context.Context
}

// report calls w.OnReport if set.
func (w *Worker) report(r Report) {
	if w.OnReport != nil {
		w.OnReport(r)
	}
}

// ctxErr returns a wrapped error if w.Ctx is set and cancelled, else nil.
func (w *Worker) ctxErr() error {
	if w.Ctx == nil {
		return nil
	}
	if err := w.Ctx.Err(); err != nil {
		return fmt.Errorf("run cancelled: %w", err)
	}
	return nil
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
	err := w.run()
	w.report(Report{Kind: ReportRunDone})
	return err
}

func (w *Worker) run() error {
	gen := w.idGen()
	for iter := 1; w.Loop.MaxIterations == 0 || iter <= w.Loop.MaxIterations; iter++ {
		if err := w.ctxErr(); err != nil {
			return err
		}
		id := gen()
		w.report(Report{Kind: ReportIteration, Iteration: iter})
		stop, err := w.runIteration(iter, id)
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
func (w *Worker) runIteration(iter int, id string) (stop bool, err error) {
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
		if err := w.ctxErr(); err != nil {
			return false, err
		}
		step := w.Loop.Steps[i]
		exec, err := w.executorFor(step)
		if err != nil {
			return false, err
		}
		w.report(Report{Kind: ReportStepStart, Step: step.Name, Iteration: iter})
		_ = rc.AppendEvent(runctx.Event{Step: step.Name, Kind: "start"})
		outcome, err := exec.Run(rc, step)
		if err != nil {
			return false, fmt.Errorf("step %q: %w", step.Name, err)
		}
		_ = rc.AppendEvent(runctx.Event{Step: step.Name, Kind: "outcome", Message: outcome.String()})
		w.report(Report{Kind: ReportOutcome, Step: step.Name, State: outcome.String(), Iteration: iter})
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
	case config.StepHeadless:
		h, err := w.resolveHarness(step)
		if err != nil {
			return nil, err
		}
		return &HeadlessExecutor{Harness: h, Prompter: w.Prompter}, nil
	case config.StepInteractive:
		h, err := w.resolveHarness(step)
		if err != nil {
			return nil, err
		}
		return &InteractiveExecutor{Harness: h, Prompter: w.Prompter, LooperBin: w.LooperBin}, nil
	default:
		return nil, fmt.Errorf("step %q: unknown type %q", step.Name, step.Type)
	}
}

// resolveHarness resolves the harness a headless/interactive step should
// use: step.Harness if set, else w.HarnessName, else Global.DefaultHarness.
func (w *Worker) resolveHarness(step config.Step) (config.Harness, error) {
	g := w.Global
	if g == nil {
		g = config.DefaultGlobal()
	}
	name := step.Harness
	if name == "" {
		name = w.HarnessName
	}
	h, err := g.ResolveHarness(name)
	if err != nil {
		return config.Harness{}, fmt.Errorf("step %q: %w", step.Name, err)
	}
	return h, nil
}
