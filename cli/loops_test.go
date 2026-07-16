package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoopsCLIIntegration is a smoke test that drives the built looper
// binary end-to-end: start the daemon, `looper start` a script-only loop,
// `looper ls`, then `looper shutdown`.
func TestLoopsCLIIntegration(t *testing.T) {
	binPath := buildLooperBinary(t)

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

	socketPath := filepath.Join(t.TempDir(), "looper.sock")

	t.Cleanup(func() {
		exec.Command(binPath, "shutdown", "--socket", socketPath).Run()
	})

	startCmd := exec.Command(binPath, "start", "t", "--socket", socketPath)
	startCmd.Dir = repo
	startCmd.Stdin = strings.NewReader("a\n") // in case of an unexpected manual prompt
	startOut, err := startCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("looper start failed: %v\n%s", err, startOut)
	}
	if !strings.Contains(string(startOut), "run done") {
		t.Errorf("start output missing terminal status; got:\n%s", startOut)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("script step did not run (marker missing): %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(repo, ".looper", "runs", "t"))
	if len(entries) != 1 {
		t.Errorf("expected 1 run dir under .looper/runs/t, got %d", len(entries))
	}

	lsCmd := exec.Command(binPath, "ls", "--socket", socketPath)
	lsOut, err := lsCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("looper ls failed: %v\n%s", err, lsOut)
	}
	if !strings.Contains(string(lsOut), "t") || !strings.Contains(string(lsOut), "done") {
		t.Errorf("ls output missing run; got:\n%s", lsOut)
	}

	shutdownCmd := exec.Command(binPath, "shutdown", "--socket", socketPath)
	if out, err := shutdownCmd.CombinedOutput(); err != nil {
		t.Fatalf("shutdown failed: %v\n%s", err, out)
	}

	// Socket should be gone shortly after shutdown.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket %s still exists after shutdown", socketPath)
}

// TestLsCmd_NoDaemonRunning verifies `looper ls` never spawns the daemon and
// exits 0 with a friendly message when it's unreachable.
func TestLsCmd_NoDaemonRunning(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "does-not-exist.sock")
	cmd := newLsCmd()
	cmd.SetArgs([]string{"--socket", socketPath})
	out := &strings.Builder{}
	cmd.SetOut(out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out.String(), "no daemon running") {
		t.Errorf("output = %q, want mention of no daemon running", out.String())
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Errorf("ls must not spawn the daemon, but socket now exists")
	}
}

// TestStopCmd_NoDaemonRunning verifies `looper stop` never spawns the
// daemon.
func TestStopCmd_NoDaemonRunning(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "does-not-exist.sock")
	cmd := newStopCmd()
	cmd.SetArgs([]string{"run-001", "--socket", socketPath})
	out := &strings.Builder{}
	cmd.SetOut(out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Errorf("stop must not spawn the daemon, but socket now exists")
	}
}
