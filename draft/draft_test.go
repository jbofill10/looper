package draft

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jbofill10/looper/config"
	looperpty "github.com/jbofill10/looper/pty"
)

// skipIfNoPTY skips t if this environment cannot allocate a pty (e.g.
// headless CI without pty support), mirroring
// runner.TestInteractive_DefaultRunUsesRealPTY's guard.
func skipIfNoPTY(t *testing.T) {
	t.Helper()
	probe, err := looperpty.Start(looperpty.Config{Argv: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Skipf("pty not available in this environment: %v", err)
	}
	_ = probe.Wait()
	_ = probe.Close()
}

func TestRun_WritesToScratchFileAndInstallsSkill(t *testing.T) {
	skipIfNoPTY(t)
	dir := t.TempDir()

	// A fake "harness" that locates the scratch file draft.Run already
	// created (before starting the session) via glob, and writes known
	// content to it, mimicking what a real harness session would do.
	h := config.Harness{Interactive: []string{
		"sh", "-c",
		`f=$(ls -t "$PWD"/.looper/tmp/draft-*.sh | head -1); printf 'echo drafted\n' > "$f"`,
	}}

	content, err := Run(dir, h, Request{LoopName: "loop", StepName: "build"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if content != "echo drafted\n" {
		t.Errorf("content = %q, want %q", content, "echo drafted\n")
	}

	skillPath := filepath.Join(dir, ".claude", "skills", "loop-creation", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("expected loop-creation skill installed at %s: %v", skillPath, err)
	}
}

func TestRun_NoInteractiveCommandErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, config.Harness{}, Request{}); err == nil {
		t.Fatal("Run: expected an error for a harness with no interactive command")
	}
}

func TestRun_SessionExitsWithoutWritingErrors(t *testing.T) {
	skipIfNoPTY(t)
	dir := t.TempDir()
	h := config.Harness{Interactive: []string{"sh", "-c", "true"}}

	if _, err := Run(dir, h, Request{}); err == nil {
		t.Fatal("Run: expected an error when the session never writes a script")
	}
}
