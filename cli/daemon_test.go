package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jbofill10/looper/client"
	"github.com/jbofill10/looper/daemon"
	"github.com/jbofill10/looper/rpc"
)

// buildLooperBinary builds the looper binary into t.TempDir() and returns its
// path. It skips the test if `go build` is unavailable or fails.
func buildLooperBinary(t *testing.T) string {
	t.Helper()

	repoRoot, err := filepath.Abs(filepath.Join("..", "."))
	if err != nil {
		t.Skipf("resolving repo root: %v", err)
	}

	binPath := filepath.Join(t.TempDir(), "looper-test-bin")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("go build unavailable or failed: %v\n%s", err, out)
	}
	return binPath
}

// waitForPing polls Dial+Ping until it succeeds or the deadline elapses,
// returning the last PingResponse on success.
func waitForPing(t *testing.T, socketPath string, timeout time.Duration) *rpc.PingResponse {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		c, conn, err := client.Dial(socketPath)
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		resp, err := c.Ping(ctx, &rpc.PingRequest{})
		cancel()
		conn.Close()
		if err == nil {
			return resp
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for daemon to become pingable at %s: %v", socketPath, lastErr)
	return nil
}

func TestDaemonPingShutdownIntegration(t *testing.T) {
	binPath := buildLooperBinary(t)

	socketPath := filepath.Join(t.TempDir(), "looper.sock")
	logPath := socketPath + ".log"
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("creating daemon log file: %v", err)
	}
	defer logFile.Close()

	daemonCmd := exec.Command(binPath, "daemon", "--socket", socketPath)
	daemonCmd.Stdout = logFile
	daemonCmd.Stderr = logFile
	daemonCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := daemonCmd.Start(); err != nil {
		t.Fatalf("starting daemon process: %v", err)
	}
	daemonProc := daemonCmd.Process
	t.Cleanup(func() {
		_ = daemonProc.Kill()
	})

	resp := waitForPing(t, socketPath, 3*time.Second)
	if resp.Version != daemon.Version {
		t.Fatalf("Ping version = %q, want %q", resp.Version, daemon.Version)
	}

	shutdownCmd := exec.Command(binPath, "shutdown", "--socket", socketPath)
	shutdownOut, err := shutdownCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shutdown command failed: %v\n%s", err, shutdownOut)
	}

	waitErrCh := make(chan error, 1)
	go func() { waitErrCh <- daemonCmd.Wait() }()
	select {
	case <-waitErrCh:
		// process exited, as expected (exit status is irrelevant here).
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for daemon process to exit after shutdown")
	}

	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket file still exists after shutdown: err=%v", err)
	}
}
