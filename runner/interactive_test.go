package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/events"
	looperpty "github.com/jbofill10/looper/pty"
)

// envLookup returns the value of key in a KEY=VALUE env slice.
func envLookup(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// sendHook dials socketPath and writes h as JSON, mimicking `looper hook`. It
// waits for the listener's ack before returning (matching forwardHook's
// behavior), so that by the time a fake `run` returns, every hook it sent is
// guaranteed to have been durably delivered to the listener's consumer.
func sendHook(t *testing.T, socketPath string, h events.Hook) {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial %s: %v", socketPath, err)
	}
	defer conn.Close()
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal hook: %v", err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	if uc, ok := conn.(interface{ CloseWrite() error }); ok {
		if err := uc.CloseWrite(); err != nil {
			t.Fatalf("close write: %v", err)
		}
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("await ack: %v", err)
	}
}

func claudeHarness() config.Harness {
	return config.DefaultGlobal().Harnesses["claude"]
}

func TestInteractive_DonePath(t *testing.T) {
	rc := newRC(t)
	h := claudeHarness()
	fp := &FakePrompter{InteractiveOutcome: OutcomeAdvance}
	exec := &InteractiveExecutor{
		Harness:  h,
		Prompter: fp,
		run: func(argv, env []string, socketPath string) error {
			sendHook(t, socketPath, events.Hook{EventName: "PreToolUse", ToolName: "Bash"})
			sendHook(t, socketPath, events.Hook{
				EventName:            "Stop",
				LastAssistantMessage: "all wrapped up " + h.Sentinels.Done,
			})
			return nil
		},
	}
	step := config.Step{Name: "session", Type: config.StepInteractive, Prompt: "do the thing"}

	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
	if fp.InteractiveCalls != 1 {
		t.Errorf("InteractiveCalls = %d, want 1", fp.InteractiveCalls)
	}
	if fp.LastInteractiveState != string(events.StateAwaitingApproval) {
		t.Errorf("LastInteractiveState = %q, want %q", fp.LastInteractiveState, events.StateAwaitingApproval)
	}
}

// TestInteractive_OnStateFiresLiveBeforeRunReturns asserts that OnState is
// called synchronously as each hook-derived state is reached, from inside
// the fake run (i.e. while the interactive session is still "running"),
// rather than only once at the end via the final outcome. This is what lets
// a caller (the daemon) learn a session is paused waiting on a human before
// the underlying process exits — the gap that previously left the fleet
// view showing a live interactive session as "running" for its whole
// duration, even once it had stopped and needed a human.
func TestInteractive_OnStateFiresLiveBeforeRunReturns(t *testing.T) {
	rc := newRC(t)
	h := claudeHarness()
	fp := &FakePrompter{InteractiveOutcome: OutcomeAdvance}

	var seenBeforeReturn []string
	var seenAfterReturn []string
	returned := false

	exec := &InteractiveExecutor{
		Harness:  h,
		Prompter: fp,
		OnState: func(state string) {
			if returned {
				seenAfterReturn = append(seenAfterReturn, state)
			} else {
				seenBeforeReturn = append(seenBeforeReturn, state)
			}
		},
		run: func(argv, env []string, socketPath string) error {
			sendHook(t, socketPath, events.Hook{EventName: "PreToolUse", ToolName: "Bash"})
			sendHook(t, socketPath, events.Hook{
				EventName:            "Stop",
				LastAssistantMessage: "all wrapped up " + h.Sentinels.Done,
			})
			return nil
		},
	}
	step := config.Step{Name: "session", Type: config.StepInteractive, Prompt: "do the thing"}

	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	returned = true

	want := []string{string(events.StateWorking), string(events.StateAwaitingApproval)}
	if len(seenBeforeReturn) != len(want) {
		t.Fatalf("OnState calls before Run returned = %v, want %v", seenBeforeReturn, want)
	}
	for i, w := range want {
		if seenBeforeReturn[i] != w {
			t.Errorf("OnState[%d] = %q, want %q", i, seenBeforeReturn[i], w)
		}
	}
	if len(seenAfterReturn) != 0 {
		t.Errorf("OnState called %d times after Run returned, want 0 (all live)", len(seenAfterReturn))
	}
}

// TestInteractive_PromptIncludesSentinelInstructions asserts that every
// interactive session's prompt is appended with instructions telling the
// model which marker to end its final message with. Loop authors were
// previously expected to embed {{SENTINEL_DONE}} etc. themselves; none of
// the shipped example loops did, so a session that genuinely finished its
// work fell through Derive's default case (StateAwaitingInput) and was
// indistinguishable from one still blocked on a question. Looper must
// supply this instruction itself rather than leave it opt-in.
func TestInteractive_PromptIncludesSentinelInstructions(t *testing.T) {
	rc := newRC(t)
	h := claudeHarness()
	fp := &FakePrompter{InteractiveOutcome: OutcomeAdvance}
	var gotPrompt string
	exec := &InteractiveExecutor{
		Harness:  h,
		Prompter: fp,
		run: func(argv, env []string, socketPath string) error {
			gotPrompt = argv[len(argv)-1]
			sendHook(t, socketPath, events.Hook{
				EventName:            "Stop",
				LastAssistantMessage: "done " + h.Sentinels.Done,
			})
			return nil
		},
	}
	step := config.Step{Name: "session", Type: config.StepInteractive, Prompt: "do the thing"}

	if _, err := exec.Run(rc, step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(gotPrompt, "do the thing") {
		t.Errorf("prompt = %q, want it to still contain the step's own prompt text", gotPrompt)
	}
	for _, marker := range []string{h.Sentinels.Done, h.Sentinels.NeedsInput, h.Sentinels.NoWork} {
		if !strings.Contains(gotPrompt, marker) {
			t.Errorf("prompt = %q, want it to contain sentinel instructions mentioning %q", gotPrompt, marker)
		}
	}
}

func TestInteractive_NoWorkPath(t *testing.T) {
	rc := newRC(t)
	h := claudeHarness()
	fp := &FakePrompter{InteractiveOutcome: OutcomeAdvance}
	exec := &InteractiveExecutor{
		Harness:  h,
		Prompter: fp,
		run: func(argv, env []string, socketPath string) error {
			sendHook(t, socketPath, events.Hook{
				EventName:            "Stop",
				LastAssistantMessage: "nothing to do " + h.Sentinels.NoWork,
			})
			return nil
		},
	}
	step := config.Step{Name: "session", Type: config.StepInteractive, Prompt: "do the thing"}

	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeNoWork {
		t.Errorf("outcome = %v, want no-work", got)
	}
	if fp.InteractiveCalls != 0 {
		t.Errorf("InteractiveCalls = %d, want 0 (prompter should not be consulted)", fp.InteractiveCalls)
	}
}

func TestInteractive_RetryPath(t *testing.T) {
	rc := newRC(t)
	h := claudeHarness()
	fp := &FakePrompter{InteractiveOutcome: OutcomeRetry}
	exec := &InteractiveExecutor{
		Harness:  h,
		Prompter: fp,
		run: func(argv, env []string, socketPath string) error {
			sendHook(t, socketPath, events.Hook{
				EventName:            "Stop",
				LastAssistantMessage: "done " + h.Sentinels.Done,
			})
			return nil
		},
	}
	step := config.Step{Name: "session", Type: config.StepInteractive, Prompt: "do the thing"}

	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeRetry {
		t.Errorf("outcome = %v, want retry", got)
	}
}

// TestInteractive_DefaultRunUsesRealPTY exercises the default run
// implementation (run==nil) end-to-end. The argv writes to a result file
// whether its stdin (fd 0) is a tty, then exits, so the final state falls
// through to the Prompter. Since the test process's own stdin is not
// generally a tty (e.g. under `go test` in CI), only a genuine PTY-backed
// default run makes the child observe a tty on fd 0; the old exec-with-
// inherited-stdio implementation would report NOTTY here. It skips if the
// environment cannot allocate a pty (e.g. headless CI without pty support).
func TestInteractive_DefaultRunUsesRealPTY(t *testing.T) {
	probe, err := looperpty.Start(looperpty.Config{Argv: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Skipf("pty not available in this environment: %v", err)
	}
	_ = probe.Wait()
	_ = probe.Close()

	resultFile := filepath.Join(t.TempDir(), "tty-check")
	rc := newRC(t)
	h := claudeHarness()
	h.Interactive = []string{"sh", "-c", fmt.Sprintf(
		`if [ -t 0 ]; then printf ISTTY > %s; else printf NOTTY > %s; fi`,
		resultFile, resultFile,
	)}
	fp := &FakePrompter{InteractiveOutcome: OutcomeAdvance}
	exec := &InteractiveExecutor{
		Harness:  h,
		Prompter: fp,
	}
	step := config.Step{Name: "session", Type: config.StepInteractive, Prompt: "do the thing"}

	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
	if fp.InteractiveCalls != 1 {
		t.Errorf("InteractiveCalls = %d, want 1", fp.InteractiveCalls)
	}

	got2, err := os.ReadFile(resultFile)
	if err != nil {
		t.Fatalf("read result file: %v", err)
	}
	if string(got2) != "ISTTY" {
		t.Errorf("child stdin tty-ness = %q, want ISTTY (default run should be pty-backed)", got2)
	}
}

func TestInteractive_CapturesOutputs(t *testing.T) {
	rc := newRC(t)
	h := claudeHarness()
	fp := &FakePrompter{InteractiveOutcome: OutcomeAdvance}
	exec := &InteractiveExecutor{
		Harness:  h,
		Prompter: fp,
		run: func(argv, env []string, socketPath string) error {
			outPath, ok := envLookup(env, "LOOPER_OUTPUT")
			if !ok {
				t.Fatal("LOOPER_OUTPUT not present in env")
			}
			if err := os.WriteFile(outPath, []byte("TASK_ID=42\n"), 0o644); err != nil {
				t.Fatalf("write outputs: %v", err)
			}
			sendHook(t, socketPath, events.Hook{
				EventName:            "Stop",
				LastAssistantMessage: "done " + h.Sentinels.Done,
			})
			return nil
		},
	}
	step := config.Step{
		Name: "session", Type: config.StepInteractive, Prompt: "do the thing",
		Outputs: []string{"TASK_ID"},
	}

	got, err := exec.Run(rc, step)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != OutcomeAdvance {
		t.Errorf("outcome = %v, want advance", got)
	}
	v, ok := rc.Get("TASK_ID")
	if !ok || v != "42" {
		t.Errorf("TASK_ID = %q, ok=%v, want 42", v, ok)
	}
}
