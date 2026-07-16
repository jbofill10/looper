package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jbofill10/looper/config"
)

// idSeq returns a deterministic id generator: "iter-1", "iter-2", ...
func idSeq() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("iter-%d", n) }
}

func newWorker(t *testing.T, loop *config.Loop, p Prompter) *Worker {
	t.Helper()
	base := filepath.Join(t.TempDir(), ".looper")
	work := t.TempDir()
	return &Worker{
		Loop:     loop,
		BaseDir:  base,
		Workdir:  work,
		Prompter: p,
		NewID:    idSeq(),
	}
}

func TestWorker_RunsUntilNoWork(t *testing.T) {
	// get-task exits 78 (no work) on the *second* iteration by using a counter file.
	counter := filepath.Join(t.TempDir(), "n")
	loop := &config.Loop{
		Name: "l",
		Steps: []config.Step{
			{
				Name: "get-task", Type: config.StepScript, SignalsNoWork: true,
				Run:     fmt.Sprintf(`n=$(cat %q 2>/dev/null || echo 0); n=$((n+1)); echo $n > %q; [ $n -ge 2 ] && exit 78; echo TASK_ID=$n >> "$LOOPER_OUTPUT"`, counter, counter),
				Outputs: []string{"TASK_ID"},
			},
			{Name: "work", Type: config.StepScript, Run: `echo "did $TASK_ID"`},
		},
	}
	if err := loop.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	w := newWorker(t, loop, &FakePrompter{})
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Iteration 1 ran fully; iteration 2 hit no-work at get-task and stopped.
	if _, err := os.Stat(filepath.Join(w.BaseDir, "runs", "l", "iter-1")); err != nil {
		t.Errorf("expected iter-1 run dir: %v", err)
	}
}

func TestWorker_MaxIterations(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 2,
		Steps:         []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, id := range []string{"iter-1", "iter-2"} {
		if _, err := os.Stat(filepath.Join(w.BaseDir, "runs", "l", id)); err != nil {
			t.Errorf("expected %s: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(w.BaseDir, "runs", "l", "iter-3")); err == nil {
		t.Errorf("iter-3 should not exist (max_iterations=2)")
	}
}

func TestWorker_OutputsFlowBetweenSteps(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps: []config.Step{
			{Name: "produce", Type: config.StepScript, Run: `echo TASK_ID=99 >> "$LOOPER_OUTPUT"`, Outputs: []string{"TASK_ID"}},
			{Name: "consume", Type: config.StepScript, Run: `echo "TASK=$TASK_ID" >> "$LOOPER_OUTPUT"`, Outputs: []string{"TASK"}},
		},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	log := filepath.Join(w.BaseDir, "runs", "l", "iter-1", "steps", "consume.outputs")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read consume outputs: %v", err)
	}
	if !strings.Contains(string(data), "TASK=99") {
		t.Errorf("consume did not see TASK_ID from produce; got %q", data)
	}
}

func TestWorker_AbortStopsIteration(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps: []config.Step{
			{Name: "boom", Type: config.StepScript, Run: "exit 1", OnFail: config.OnFailAbort},
			{Name: "never", Type: config.StepScript, Run: `echo ran >> "$LOOPER_OUTPUT"`},
		},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.BaseDir, "runs", "l", "iter-1", "steps", "never.log")); err == nil {
		t.Errorf("second step should not have run after abort")
	}
}

func TestWorker_HeadlessStepRunsToCompletion(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "plan", Type: config.StepHeadless, Prompt: "echo hi"}},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	w.Global = &config.Global{
		DefaultHarness: "stub",
		Harnesses: map[string]config.Harness{
			"stub": {Headless: []string{"sh", "-c", "{{PROMPT}}"}},
		},
	}
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	dir := filepath.Join(w.BaseDir, "runs", "l", "iter-1", "events.jsonl")
	data, err := os.ReadFile(dir)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(data), "advance") {
		t.Errorf("expected advance outcome in events; got %q", data)
	}
}

func TestWorker_ExecutorForInteractive(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "plan", Type: config.StepInteractive, Prompt: "hi"}},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	w.LooperBin = "/usr/local/bin/looper"

	exec, err := w.executorFor(loop.Steps[0])
	if err != nil {
		t.Fatalf("executorFor: %v", err)
	}
	ie, ok := exec.(*InteractiveExecutor)
	if !ok {
		t.Fatalf("executorFor returned %T, want *InteractiveExecutor", exec)
	}
	if ie.LooperBin != w.LooperBin {
		t.Errorf("LooperBin = %q, want %q", ie.LooperBin, w.LooperBin)
	}
	if ie.Harness.Interactive == nil {
		t.Errorf("Harness not resolved: %+v", ie.Harness)
	}
}

func TestWorker_ExecutorForInteractiveUsesInjectedRun(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "plan", Type: config.StepInteractive, Prompt: "hi"}},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{InteractiveOutcome: OutcomeAdvance})

	ranCh := make(chan struct{}, 1)
	w.InteractiveRun = func(argv, env []string, socketPath string) error {
		ranCh <- struct{}{}
		return nil
	}

	exec, err := w.executorFor(loop.Steps[0])
	if err != nil {
		t.Fatalf("executorFor: %v", err)
	}
	ie, ok := exec.(*InteractiveExecutor)
	if !ok {
		t.Fatalf("executorFor returned %T, want *InteractiveExecutor", exec)
	}
	if ie.run == nil {
		t.Fatalf("expected ie.run to be set from Worker.InteractiveRun")
	}

	if _, err := ie.Run(newRC(t), loop.Steps[0]); err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case <-ranCh:
	default:
		t.Errorf("injected InteractiveRun was not invoked")
	}
}

func TestWorker_OnReportRecordsSequence(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})

	var kinds []string
	w.OnReport = func(r Report) {
		kinds = append(kinds, r.Kind)
	}
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{ReportIteration, ReportStepStart, ReportOutcome, ReportRunDone}
	if len(kinds) != len(want) {
		t.Fatalf("report kinds = %v, want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Errorf("kinds[%d] = %q, want %q", i, kinds[i], k)
		}
	}
}

func TestWorker_CtxCancelledStopsBeforeFurtherSteps(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 0,
		Steps: []config.Step{
			{Name: "s1", Type: config.StepScript, Run: "true"},
			{Name: "s2", Type: config.StepScript, Run: `echo ran >> "$LOOPER_OUTPUT"`},
		},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run starts
	w.Ctx = ctx

	err := w.Run()
	if err == nil {
		t.Fatalf("expected error from cancelled context, got nil")
	}
	if _, err := os.Stat(filepath.Join(w.BaseDir, "runs", "l", "iter-1")); err == nil {
		t.Errorf("expected no run dir to be created after immediate cancellation")
	}
}

func TestWorker_ReportsWorkerIDAndTask(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps: []config.Step{
			{Name: "get-task", Type: config.StepScript, Run: `echo TASK_ID=42 >> "$LOOPER_OUTPUT"`, Outputs: []string{"TASK_ID"}},
			{Name: "work", Type: config.StepScript, Run: "true"},
		},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	w.ID = "w7"

	var reports []Report
	w.OnReport = func(r Report) { reports = append(reports, r) }
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, r := range reports {
		if r.WorkerID != "w7" {
			t.Errorf("report %+v: WorkerID = %q, want w7", r, r.WorkerID)
		}
	}

	var sawGetTaskOutcome, sawWorkStep bool
	for _, r := range reports {
		if r.Kind == ReportOutcome && r.Step == "get-task" {
			sawGetTaskOutcome = true
			if r.Task != "42" {
				t.Errorf("get-task outcome Task = %q, want 42", r.Task)
			}
		}
		if r.Step == "work" {
			sawWorkStep = true
			if r.Task != "42" {
				t.Errorf("work step report Task = %q, want 42 (set by prior step)", r.Task)
			}
		}
	}
	if !sawGetTaskOutcome {
		t.Fatalf("no outcome report seen for get-task")
	}
	if !sawWorkStep {
		t.Fatalf("no report seen for work step")
	}
}

func TestWorker_CustomTaskVar(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps: []config.Step{
			{Name: "get-task", Type: config.StepScript, Run: `echo ISSUE_ID=7 >> "$LOOPER_OUTPUT"`, Outputs: []string{"ISSUE_ID"}},
		},
	}
	_ = loop.Validate()
	w := newWorker(t, loop, &FakePrompter{})
	w.TaskVar = "ISSUE_ID"

	var last Report
	w.OnReport = func(r Report) {
		if r.Kind == ReportOutcome {
			last = r
		}
	}
	if err := w.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if last.Task != "7" {
		t.Errorf("Task = %q, want 7 (via custom TaskVar)", last.Task)
	}
}

// TestWorker_AcquireLockSerializesAcrossWorkers runs two workers concurrently
// sharing an AcquireLock and a shell get-task script backed by a shared
// counter file: each pull atomically appends its pulled task id to a sink
// file and exits 78 once K tasks have been handed out. Because the shared
// mutex wraps only the signals_no_work step, the two workers' script
// invocations never overlap, so the counter/sink read-modify-write is safe
// without any locking inside the script itself. Assert every worker sees a
// unique set of task ids with no duplicates and no gaps.
func TestWorker_AcquireLockSerializesAcrossWorkers(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "counter")
	sink := filepath.Join(dir, "sink")
	const k = 6

	script := fmt.Sprintf(`
n=$(cat %q 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > %q
if [ "$n" -gt %d ]; then exit 78; fi
echo "$n" >> %q
echo TASK_ID=$n >> "$LOOPER_OUTPUT"
`, counter, counter, k, sink)

	loop := &config.Loop{
		Name: "l",
		Steps: []config.Step{
			{Name: "get-task", Type: config.StepScript, SignalsNoWork: true, Run: script, Outputs: []string{"TASK_ID"}},
			{Name: "work", Type: config.StepScript, Run: "true"},
		},
	}
	if err := loop.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var lock sync.Mutex
	base := filepath.Join(dir, ".looper")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"w1", "w2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			w := &Worker{
				Loop:        loop,
				BaseDir:     base,
				Workdir:     dir,
				Prompter:    &FakePrompter{},
				ID:          id,
				AcquireLock: &lock,
			}
			errs <- w.Run()
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	}

	data, err := os.ReadFile(sink)
	if err != nil {
		t.Fatalf("read sink: %v", err)
	}
	lines := strings.Fields(strings.TrimSpace(string(data)))
	seen := map[string]bool{}
	for _, l := range lines {
		if seen[l] {
			t.Fatalf("task id %q pulled more than once; sink contents: %q", l, data)
		}
		seen[l] = true
	}
	if len(seen) != k {
		t.Fatalf("got %d distinct task ids, want %d; sink contents: %q", len(seen), k, data)
	}
	for i := 1; i <= k; i++ {
		if !seen[fmt.Sprintf("%d", i)] {
			t.Errorf("task id %d never pulled; sink contents: %q", i, data)
		}
	}
}

func TestWorker_UnknownStepTypeErrors(t *testing.T) {
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "plan", Type: "bogus", Prompt: "hi"}},
	}
	w := newWorker(t, loop, &FakePrompter{})
	_, err := w.executorFor(loop.Steps[0])
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("expected 'unknown type' error, got %v", err)
	}
}
