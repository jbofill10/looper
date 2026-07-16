package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/pty"
	"gopkg.in/yaml.v3"
)

// drainTimeout bounds how long tests wait for an expected Update; it guards
// against a hung test rather than masking a race.
const drainTimeout = 5 * time.Second

func writeLoopFile(t *testing.T, dir string, loop *config.Loop) string {
	t.Helper()
	data, err := yaml.Marshal(loop)
	if err != nil {
		t.Fatalf("marshal loop: %v", err)
	}
	path := filepath.Join(dir, loop.Name+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write loop file: %v", err)
	}
	return path
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(nil, "looper")
}

func recvUpdate(t *testing.T, ch <-chan Update) Update {
	t.Helper()
	select {
	case u, ok := <-ch:
		if !ok {
			t.Fatalf("update channel closed unexpectedly")
		}
		return u
	case <-time.After(drainTimeout):
		t.Fatalf("timed out waiting for an update")
	}
	panic("unreachable")
}

func drainUntilRunDone(t *testing.T, ch <-chan Update) []Update {
	t.Helper()
	var got []Update
	for {
		u := recvUpdate(t, ch)
		got = append(got, u)
		if u.Kind == "run_done" {
			return got
		}
	}
}

func TestManager_ScriptLoopRunsToCompletion(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	}
	path := writeLoopFile(t, dir, loop)

	m := newTestManager(t)
	ch, unsub := m.Subscribe("")
	defer unsub()

	runID, err := m.StartLoop("", path, filepath.Join(dir, ".looper"), dir)
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}
	if runID == "" {
		t.Fatalf("expected non-empty run id")
	}

	updates := drainUntilRunDone(t, ch)
	var kinds []string
	for _, u := range updates {
		kinds = append(kinds, u.Kind)
	}
	found := map[string]bool{}
	for _, k := range kinds {
		found[k] = true
	}
	for _, want := range []string{"step_start", "outcome", "run_done"} {
		if !found[want] {
			t.Errorf("expected an update of kind %q among %v", want, kinds)
		}
	}

	runs := m.ListRuns()
	if len(runs) != 1 {
		t.Fatalf("ListRuns = %v, want 1 run", runs)
	}
	if runs[0].Status != "done" {
		t.Errorf("Status = %q, want done", runs[0].Status)
	}
}

func TestManager_ManualStepDecisionRequestAdvance(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "gate", Type: config.StepManual}},
	}
	path := writeLoopFile(t, dir, loop)

	m := newTestManager(t)
	ch, unsub := m.Subscribe("")
	defer unsub()

	runID, err := m.StartLoop("", path, filepath.Join(dir, ".looper"), dir)
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}

	var reqID string
	for {
		u := recvUpdate(t, ch)
		if u.Kind == "decision_request" {
			reqID = u.RequestID
			break
		}
	}
	if reqID == "" {
		t.Fatalf("expected a decision_request with a request id")
	}

	if err := m.Respond(runID, reqID, "advance"); err != nil {
		t.Fatalf("Respond: %v", err)
	}

	updates := drainUntilRunDone(t, ch)
	last := updates[len(updates)-1]
	if last.State != "done" {
		t.Errorf("run_done State = %q, want done", last.State)
	}
	runs := m.ListRuns()
	if len(runs) != 1 || runs[0].Status != "done" {
		t.Errorf("ListRuns = %v, want single done run", runs)
	}
}

func TestManager_RespondAbortEndsRun(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name:          "l",
		MaxIterations: 1,
		Steps:         []config.Step{{Name: "gate", Type: config.StepManual}},
	}
	path := writeLoopFile(t, dir, loop)

	m := newTestManager(t)
	ch, unsub := m.Subscribe("")
	defer unsub()

	runID, err := m.StartLoop("", path, filepath.Join(dir, ".looper"), dir)
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}

	var reqID string
	for {
		u := recvUpdate(t, ch)
		if u.Kind == "decision_request" {
			reqID = u.RequestID
			break
		}
	}

	if err := m.Respond(runID, reqID, "abort"); err != nil {
		t.Fatalf("Respond: %v", err)
	}

	drainUntilRunDone(t, ch) // must not panic and must terminate

	runs := m.ListRuns()
	if len(runs) != 1 || runs[0].Status != "done" {
		t.Errorf("ListRuns = %v, want single done run (abort ends the iteration, not the run)", runs)
	}
}

func TestManager_StopLoopMidRun(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name: "l",
		Steps: []config.Step{
			{Name: "sleep", Type: config.StepScript, Run: "sleep 0.2"},
		},
	}
	path := writeLoopFile(t, dir, loop)

	m := newTestManager(t)
	ch, unsub := m.Subscribe("")
	defer unsub()

	runID, err := m.StartLoop("", path, filepath.Join(dir, ".looper"), dir)
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}

	// Wait for the run to actually start (first step_start) before stopping,
	// so the cancellation is observed between iterations rather than before
	// the worker even begins.
	for {
		u := recvUpdate(t, ch)
		if u.Kind == "step_start" {
			break
		}
	}

	if err := m.StopLoop(runID); err != nil {
		t.Fatalf("StopLoop: %v", err)
	}

	drainUntilRunDone(t, ch)

	runs := m.ListRuns()
	if len(runs) != 1 || runs[0].Status != "stopped" {
		t.Errorf("ListRuns = %v, want single stopped run", runs)
	}
}

// catHarnessGlobal returns a Global whose default harness's interactive
// command is `sh -c cat`, which echoes stdin to stdout. BuildInteractive
// appends "--settings <file> <prompt>" positional args, which cat ignores.
func catHarnessGlobal() *config.Global {
	return &config.Global{
		DefaultHarness: "catty",
		Harnesses: map[string]config.Harness{
			"catty": {Interactive: []string{"sh", "-c", "cat"}},
		},
	}
}

// syncBuffer is a *bytes.Buffer guarded by a mutex, safe for concurrent
// Write (from the session's reader goroutine) and String (from the test
// goroutine).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForSession polls Manager.Session(runID) (bounded) until it becomes
// live, failing the test if it doesn't in time.
func waitForSession(t *testing.T, m *Manager, runID string) *pty.Session {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sess, ok := m.Session(runID); ok {
			return sess
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session for run %q never became live", runID)
	return nil
}

// waitForSyncBuf spin-waits (bounded) until buf contains substr.
func waitForSyncBuf(t *testing.T, buf *syncBuffer, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), substr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("buffer never contained %q; got %q", substr, buf.String())
}

func TestManager_StartLoopRunsInteractiveSessionAndRegistersIt(t *testing.T) {
	dir := t.TempDir()
	loop := &config.Loop{
		Name: "l",
		Steps: []config.Step{
			{Name: "sess", Type: config.StepInteractive, Prompt: "hi"},
		},
	}
	path := writeLoopFile(t, dir, loop)

	m := NewManager(catHarnessGlobal(), "looper")
	runID, err := m.StartLoop("", path, filepath.Join(dir, ".looper"), dir)
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}

	sess := waitForSession(t, m, runID)

	buf := &syncBuffer{}
	stop := sess.PipeTo(buf)
	defer stop()

	if _, err := sess.Write([]byte("ping\n")); err != nil {
		t.Fatalf("Write to session: %v", err)
	}
	waitForSyncBuf(t, buf, "ping")

	if err := m.StopLoop(runID); err != nil {
		t.Fatalf("StopLoop: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.Session(runID); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := m.Session(runID); ok {
		t.Fatalf("session still registered after StopLoop")
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs := m.ListRuns()
		if len(runs) == 1 && runs[0].Status != "running" {
			if runs[0].Status != "stopped" && runs[0].Status != "error" {
				t.Fatalf("run status = %q, want stopped or error after StopLoop", runs[0].Status)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run did not finish after StopLoop")
}
