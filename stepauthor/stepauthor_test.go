package stepauthor

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jbofill10/looper/config"
	looperpty "github.com/jbofill10/looper/pty"
)

// skipIfNoPTY skips t if this environment cannot allocate a pty, mirroring
// runner and the former draft package's guard.
func skipIfNoPTY(t *testing.T) {
	t.Helper()
	probe, err := looperpty.Start(looperpty.Config{Argv: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Skipf("pty not available in this environment: %v", err)
	}
	_ = probe.Wait()
	_ = probe.Close()
}

func TestCreateStep_RunsSessionInProjectDir(t *testing.T) {
	skipIfNoPTY(t)
	dir := t.TempDir()

	// A fake "harness" that writes a known marker file into the project
	// directory it was started in, standing in for a real claude session
	// editing the loop file.
	h := config.Harness{Interactive: []string{
		"sh", "-c", `touch "$PWD"/create-step-ran`,
	}}

	if err := CreateStep(dir, h, dir+"/.looper/loops/x.yaml"); err != nil {
		t.Fatalf("CreateStep: %v", err)
	}
	if _, err := os.Stat(dir + "/create-step-ran"); err != nil {
		t.Errorf("expected session to run in %s: %v", dir, err)
	}
}

func TestEditStep_IncludesValidationErrorInPrompt(t *testing.T) {
	skipIfNoPTY(t)
	dir := t.TempDir()

	// Echo the prompt (claude's trailing positional arg) to a file so the
	// test can inspect what EditStep told the session. BuildStepAuthoring
	// appends "--plugin-dir", pluginDir, prompt after h.Interactive, and
	// the trailing "--" here becomes $0 for the sh -c script, so the
	// prompt lands in $3 ($1=--plugin-dir, $2=pluginDir).
	h := config.Harness{Interactive: []string{
		"sh", "-c", `printf '%s' "$3" > "$PWD"/prompt.txt`, "--",
	}}

	err := EditStep(dir, h, dir+"/.looper/loops/x.yaml", "deploy",
		fmt.Errorf("interactive step requires 'prompt'"))
	if err != nil {
		t.Fatalf("EditStep: %v", err)
	}

	got, err := os.ReadFile(dir + "/prompt.txt")
	if err != nil {
		t.Fatalf("reading prompt.txt: %v", err)
	}
	for _, want := range []string{"deploy", "interactive step requires 'prompt'"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("prompt = %q, want it to contain %q", got, want)
		}
	}
}

func TestCreateStep_NoInteractiveCommandErrors(t *testing.T) {
	dir := t.TempDir()
	if err := CreateStep(dir, config.Harness{}, dir+"/x.yaml"); err == nil {
		t.Fatal("expected an error for a harness with no interactive command")
	}
}
