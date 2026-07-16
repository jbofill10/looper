package cli

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jbofill10/looper/builder"
	"github.com/jbofill10/looper/config"

	tea "github.com/charmbracelet/bubbletea"
)

// driveToDone feeds keys to m via Update to complete the single-page
// guided builder form with one manual step named "review", simulating a
// user filling it out via tab navigation and the step-type select field
// (right cycles script -> headless -> interactive -> manual).
func driveToDone(t *testing.T, m builder.Model, name string) builder.Model {
	t.Helper()
	send := func(msg tea.Msg) {
		next, _ := m.Update(msg)
		mm, ok := next.(builder.Model)
		if !ok {
			t.Fatalf("Update did not return a builder.Model")
		}
		m = mm
	}
	typeStr := func(s string) {
		for _, r := range s {
			send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
	tab := func() { send(tea.KeyMsg{Type: tea.KeyTab}) }
	right := func() { send(tea.KeyMsg{Type: tea.KeyRight}) }
	enter := func() { send(tea.KeyMsg{Type: tea.KeyEnter}) }

	typeStr(name) // loop name
	tab()         // -> concurrency (left blank)
	tab()         // -> step name
	typeStr("review")
	tab() // -> step type (defaults to script)
	right()
	right()
	right() // script -> headless -> interactive -> manual
	tab()   // -> outputs (left blank)
	tab()   // -> add step
	enter()

	// After adding, focus resets to step name and the step type defaults
	// back to script, so its full field list (run/draft/on_fail) is
	// visible again on the way to finish.
	for i := 0; i < 7; i++ {
		tab()
	}
	enter() // finish

	if !m.Done() {
		t.Fatalf("driveToDone: builder did not finish")
	}
	return m
}

func TestBuildAndSave(t *testing.T) {
	dir := t.TempDir()
	m := driveToDone(t, builder.New(nil, nil, builder.Options{}), "my-loop")

	path, err := buildAndSave(m, dir)
	if err != nil {
		t.Fatalf("buildAndSave: %v", err)
	}
	wantPath := filepath.Join(dir, ".looper", "loops", "my-loop.yaml")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}

	loaded, err := config.LoadLoop(path)
	if err != nil {
		t.Fatalf("LoadLoop: %v", err)
	}
	if loaded.Name != "my-loop" {
		t.Errorf("name = %q, want my-loop", loaded.Name)
	}
	if len(loaded.Steps) != 1 || loaded.Steps[0].Name != "review" {
		t.Errorf("steps = %+v, want one step named review", loaded.Steps)
	}
}

func TestBuildAndSave_NotDoneErrors(t *testing.T) {
	dir := t.TempDir()
	m := builder.New(nil, nil, builder.Options{}) // form not yet finished

	if _, err := buildAndSave(m, dir); err == nil {
		t.Fatal("buildAndSave: expected error for a builder that has not finished, got nil")
	}
}

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
