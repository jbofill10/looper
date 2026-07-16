package cli

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestBareCommand_NonTTYPrintsHintAndExits exercises the bare `looper`
// invocation (no subcommand) via the built binary with piped (non-tty)
// stdin: since launching the interactive TUI on a non-terminal would hang
// forever, it must instead print a hint and exit 0.
func TestBareCommand_NonTTYPrintsHintAndExits(t *testing.T) {
	binPath := buildLooperBinary(t)

	cmd := exec.Command(binPath)
	cmd.Stdin = strings.NewReader("")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting looper: %v", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("bare looper exited with error: %v\noutput: %s", err, out.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("bare looper did not exit within 5s (would have hung on a non-tty); output: %s", out.String())
	}

	if !strings.Contains(out.String(), "not a terminal") {
		t.Fatalf("bare looper on non-tty output = %q, want it to mention it is not a terminal", out.String())
	}
}

// TestTuiCmd_NonTTYPrintsHintAndExits exercises `looper tui` explicitly via
// the built binary with piped stdin, mirroring the bare-command behavior.
func TestTuiCmd_NonTTYPrintsHintAndExits(t *testing.T) {
	binPath := buildLooperBinary(t)

	cmd := exec.Command(binPath, "tui")
	cmd.Stdin = strings.NewReader("")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting looper tui: %v", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("looper tui exited with error: %v\noutput: %s", err, out.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("looper tui did not exit within 5s (would have hung on a non-tty); output: %s", out.String())
	}

	if !strings.Contains(out.String(), "not a terminal") {
		t.Fatalf("looper tui on non-tty output = %q, want it to mention it is not a terminal", out.String())
	}
}
