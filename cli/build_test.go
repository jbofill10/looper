package cli

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jbofill10/looper/config"
)

// TestNewCmd_NonTTYPrintsHintAndExits exercises `looper new` via the built
// binary with piped (non-tty) stdin: launching the interactive builder on
// a non-terminal would hang forever, so it must print a hint and exit 0.
func TestNewCmd_NonTTYPrintsHintAndExits(t *testing.T) {
	binPath := buildLooperBinary(t)

	cmd := exec.Command(binPath, "new", "some-loop")
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader("")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting looper new: %v", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("looper new exited with error: %v\noutput: %s", err, out.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("looper new did not exit within 5s (would have hung on a non-tty); output: %s", out.String())
	}

	if !strings.Contains(out.String(), "not a terminal") {
		t.Fatalf("looper new on non-tty output = %q, want it to mention it is not a terminal", out.String())
	}
}

// TestEditCmd_NonTTYPrintsHintAndExits exercises `looper edit` via the built
// binary with piped stdin, mirroring the new-command behavior.
func TestEditCmd_NonTTYPrintsHintAndExits(t *testing.T) {
	binPath := buildLooperBinary(t)

	dir := t.TempDir()
	loopPath := filepath.Join(dir, ".looper", "loops", "some-loop.yaml")
	existing := &config.Loop{
		Name: "some-loop",
		Steps: []config.Step{
			{Name: "review", Type: config.StepManual},
		},
	}
	if err := config.SaveLoop(existing, loopPath); err != nil {
		t.Fatalf("SaveLoop: %v", err)
	}

	cmd := exec.Command(binPath, "edit", "some-loop")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting looper edit: %v", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("looper edit exited with error: %v\noutput: %s", err, out.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("looper edit did not exit within 5s (would have hung on a non-tty); output: %s", out.String())
	}

	if !strings.Contains(out.String(), "not a terminal") {
		t.Fatalf("looper edit on non-tty output = %q, want it to mention it is not a terminal", out.String())
	}
}
