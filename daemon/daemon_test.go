package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/rpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// waitForSocket polls until the unix socket at path is dialable or the
// deadline elapses.
func waitForSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for socket %s to be dialable", path)
}

func dial(t *testing.T, path string) (rpc.LooperClient, *grpc.ClientConn) {
	t.Helper()
	conn, err := grpc.NewClient("unix://"+path, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	return rpc.NewLooperClient(conn), conn
}

func TestServePingShutdown(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "looper.sock")

	s := New()
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- s.Serve(socketPath)
	}()

	waitForSocket(t, socketPath, 2*time.Second)

	client, conn := dial(t, socketPath)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := client.Ping(ctx, &rpc.PingRequest{})
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if resp.Version != Version {
		t.Fatalf("Ping version = %q, want %q", resp.Version, Version)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if _, err := client.Shutdown(shutdownCtx, &rpc.ShutdownRequest{}); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	conn.Close()

	select {
	case err := <-serveErrCh:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Serve to return after Shutdown")
	}

	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket file still exists after shutdown: err=%v", err)
	}
}

func TestServeRemovesStaleSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "looper.sock")

	// Create a stale socket file (not an actual listening socket).
	f, err := os.Create(socketPath)
	if err != nil {
		t.Fatalf("creating stale socket file: %v", err)
	}
	f.Close()

	s := New()
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- s.Serve(socketPath)
	}()

	waitForSocket(t, socketPath, 2*time.Second)

	s.Stop()

	select {
	case err := <-serveErrCh:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Serve to return after Stop")
	}
}

func TestServer_AutoResumeDelegatesToManager(t *testing.T) {
	dir := t.TempDir()
	loopsDir := filepath.Join(dir, ".looper", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLoopFile(t, loopsDir, &config.Loop{
		Name: "a", MaxIterations: 1, Steps: []config.Step{{Name: "s", Type: config.StepScript, Run: "true"}},
	})

	s := New()
	s.manager.SetRegistryPath(filepath.Join(t.TempDir(), "state.json"))
	if _, err := s.manager.SetLoopEnabled("a", filepath.Join(dir, ".looper"), dir, true); err != nil {
		t.Fatalf("seed enable: %v", err)
	}
	// Simulate a fresh daemon process picking the same registry back up.
	s2 := New()
	s2.manager.SetRegistryPath(s.manager.registryPath)
	if errs := s2.AutoResume(); len(errs) != 0 {
		t.Fatalf("AutoResume errors: %v", errs)
	}
	// Drain updates until all runs finish (both from s and s2's AutoResume).
	ch, unsub := s2.manager.Subscribe("")
	defer unsub()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case u, ok := <-ch:
			if !ok {
				return
			}
			if u.Kind == "run_done" {
				return
			}
		case <-time.After(time.Until(deadline)):
			return
		}
	}
}
