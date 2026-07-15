package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLoop_ExecutesScriptLoop(t *testing.T) {
	repo := t.TempDir()
	loopDir := filepath.Join(repo, ".looper", "loops")
	if err := os.MkdirAll(loopDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo, "ran.txt")
	loopYAML := "name: t\nmax_iterations: 1\nsteps:\n" +
		"  - name: do\n    type: script\n    run: \"echo hello > " + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(loopDir, "t.yaml"), []byte(loopYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RunLoop(RunOptions{
		LoopName: "t",
		BaseDir:  filepath.Join(repo, ".looper"),
		Workdir:  repo,
		In:       strings.NewReader(""),
		Out:      &strings.Builder{},
	})
	if err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("script step did not run (marker missing): %v", err)
	}
	// Run dir was created under BaseDir/runs/t.
	entries, _ := os.ReadDir(filepath.Join(repo, ".looper", "runs", "t"))
	if len(entries) != 1 {
		t.Errorf("expected 1 iteration run dir, got %d", len(entries))
	}
}

func TestRunLoop_MissingLoopErrors(t *testing.T) {
	err := RunLoop(RunOptions{
		LoopName: "nope",
		BaseDir:  filepath.Join(t.TempDir(), ".looper"),
		Out:      &strings.Builder{},
	})
	if err == nil {
		t.Fatal("expected error for missing loop file")
	}
}
