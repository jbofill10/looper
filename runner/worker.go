package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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

	// WorkerID identifies which Worker emitted this report; empty for a
	// Worker with no ID set (preserves single-worker behavior).
	WorkerID string
	// Task is the current value of the loop's task var (Worker.TaskVar),
	// looked up after the acquisition step has run; empty until then.
	Task string
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

	// ID identifies this worker among others in the same run (e.g. "w1",
	// "w2"). Empty preserves prior single-worker behavior: reports carry
	// no WorkerID and iteration run dirs are not namespaced.
	ID string
	// TaskVar is the loop output var holding the current work unit's
	// identity, looked up after the acquisition step runs. Empty defaults
	// to "TASK_ID".
	TaskVar string
	// AcquireLock, if set, is held for the duration of any step whose
	// SignalsNoWork is true, serializing task acquisition across the
	// workers of a run that share the same lock. Nil preserves prior
	// unserialized behavior.
	AcquireLock sync.Locker

	// InteractiveRun, if set, replaces the default local-pty run
	// implementation (runPTY) for interactive steps. The daemon injects an
	// implementation that starts a pty.Session and registers it on the run
	// for remote attach, instead of auto-attaching to the daemon's own
	// stdio. A nil value preserves the local `looper run` behavior
	// unchanged.
	InteractiveRun func(argv, env []string, socketPath string) error

	// OnReport, if non-nil, is called synchronously as the worker makes
	// progress: at the start of each iteration, at each step start, after
	// each step outcome, and once when the run loop ends.
	OnReport func(Report)

	// Ctx, if non-nil, is checked before each step and between iterations;
	// once cancelled, Run returns Ctx.Err() promptly.
	Ctx context.Context

	// ResumeDir, if set, names an existing iteration directory (with a
	// context.json and, optionally, a progress.json) to resume instead of
	// starting the first iteration fresh: the persisted context is loaded,
	// steps already recorded as Completed in its Progress are skipped (each
	// emitting a ReportOutcome with State "resumed-skip"), and execution
	// continues from the first step not yet completed. Only the first
	// iteration resumes; ResumeDir is consumed afterward and later
	// iterations start fresh as usual.
	ResumeDir string
}

// report calls w.OnReport if set, tagging r with the worker's id.
func (w *Worker) report(r Report) {
	if w.OnReport == nil {
		return
	}
	r.WorkerID = w.ID
	w.OnReport(r)
}

// reportTask is like report but additionally tags r with the current task
// (the value of w.taskVar() in rc), reflecting whatever the acquisition step
// has set so far. rc may be nil (before an iteration's RunContext exists).
func (w *Worker) reportTask(rc *runctx.RunContext, r Report) {
	if w.OnReport == nil {
		return
	}
	if rc != nil {
		if v, ok := rc.Get(w.taskVar()); ok {
			r.Task = v
		}
	}
	w.report(r)
}

// taskVar returns w.TaskVar, defaulting to "TASK_ID" when unset.
func (w *Worker) taskVar() string {
	if w.TaskVar == "" {
		return "TASK_ID"
	}
	return w.TaskVar
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
	resumeDir := w.ResumeDir
	for iter := 1; w.Loop.MaxIterations == 0 || iter <= w.Loop.MaxIterations; iter++ {
		if err := w.ctxErr(); err != nil {
			return err
		}
		id := gen()
		w.report(Report{Kind: ReportIteration, Iteration: iter})
		stop, err := w.runIteration(iter, id, resumeDir)
		resumeDir = "" // only the first iteration may resume
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// stepCompleted reports whether name appears in completed.
func stepCompleted(completed []string, name string) bool {
	for _, c := range completed {
		if c == name {
			return true
		}
	}
	return false
}

// runIteration runs all steps for one work unit. It returns stop=true when the
// loop should end (no-work signalled). If resumeDir is non-empty, the
// iteration resumes from that directory's persisted context and progress
// instead of starting fresh: steps already marked Completed are skipped
// (reporting "resumed-skip") and execution continues from the first
// incomplete step, reusing the persisted context.
func (w *Worker) runIteration(iter int, id string, resumeDir string) (stop bool, err error) {
	resuming := resumeDir != ""

	var dir string
	var rc *runctx.RunContext
	var progress runctx.Progress

	if resuming {
		dir = resumeDir
		rc, err = runctx.Load(dir)
		if err != nil {
			return false, fmt.Errorf("resume %s: %w", dir, err)
		}
		progress, err = rc.LoadProgress()
		if err != nil {
			return false, fmt.Errorf("resume %s: %w", dir, err)
		}
		if v, ok := rc.Get("WORKDIR"); !ok || v == "" {
			rc.Set("WORKDIR", w.Workdir)
		}
	} else {
		dir = filepath.Join(w.BaseDir, "runs", w.Loop.Name, id)
		if w.ID != "" {
			dir = filepath.Join(w.BaseDir, "runs", w.Loop.Name, w.ID, id)
		}
		rc, err = runctx.New(dir)
		if err != nil {
			return false, err
		}
		rc.Set("WORKDIR", w.Workdir)
	}

	var digest strings.Builder
	fmt.Fprintf(&digest, "# Iteration %s\n\n", filepath.Base(dir))

	completed := append([]string(nil), progress.Completed...)

	i := 0
	if resuming {
		for i < len(w.Loop.Steps) && stepCompleted(progress.Completed, w.Loop.Steps[i].Name) {
			step := w.Loop.Steps[i]
			w.reportTask(rc, Report{Kind: ReportOutcome, Step: step.Name, State: "resumed-skip", Iteration: iter})
			fmt.Fprintf(&digest, "- %s → resumed-skip\n", step.Name)
			i++
		}
	}

	for i < len(w.Loop.Steps) {
		if err := w.ctxErr(); err != nil {
			return false, err
		}
		step := w.Loop.Steps[i]
		exec, err := w.executorFor(step)
		if err != nil {
			return false, err
		}
		w.reportTask(rc, Report{Kind: ReportStepStart, Step: step.Name, Iteration: iter})
		_ = rc.AppendEvent(runctx.Event{Step: step.Name, Kind: "start"})
		outcome, err := w.runStep(exec, rc, step)
		if err != nil {
			return false, fmt.Errorf("step %q: %w", step.Name, err)
		}
		_ = rc.AppendEvent(runctx.Event{Step: step.Name, Kind: "outcome", Message: outcome.String()})
		w.reportTask(rc, Report{Kind: ReportOutcome, Step: step.Name, State: outcome.String(), Iteration: iter})
		fmt.Fprintf(&digest, "- %s → %s\n", step.Name, outcome)

		if err := rc.Save(); err != nil {
			return false, err
		}

		switch outcome {
		case OutcomeAdvance:
			i++
			completed = append(completed, step.Name)
			if err := rc.SaveProgress(runctx.Progress{Completed: completed, Done: false}); err != nil {
				return false, err
			}
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
	if err := rc.SaveProgress(runctx.Progress{Completed: completed, Done: true}); err != nil {
		return false, err
	}
	return false, rc.WriteDigest(digest.String())
}

// runStep executes exec against step, holding w.AcquireLock (if set) for the
// duration of the call when step.SignalsNoWork is true. This serializes task
// acquisition across the workers of a run that share the same lock, without
// holding it for the rest of the step's own work.
func (w *Worker) runStep(exec Executor, rc *runctx.RunContext, step config.Step) (Outcome, error) {
	if step.SignalsNoWork && w.AcquireLock != nil {
		w.AcquireLock.Lock()
		defer w.AcquireLock.Unlock()
	}
	return exec.Run(rc, step)
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
		return &InteractiveExecutor{Harness: h, Prompter: w.Prompter, LooperBin: w.LooperBin, run: w.InteractiveRun}, nil
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
