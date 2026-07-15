package client

import (
	"path/filepath"
	"testing"

	"github.com/jbofill10/looper/daemon"
)

func TestSocketPathUsesXDGRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	got := SocketPath()
	want := filepath.Join("/run/user/1000", "looper.sock")
	if got != want {
		t.Fatalf("SocketPath() = %q, want %q", got, want)
	}
}

func TestSocketPathFallsBackWithoutXDGRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	got := SocketPath()
	if got == "" {
		t.Fatal("SocketPath() returned empty string")
	}
	if filepath.Dir(got) == "/run/user/1000" {
		t.Fatalf("SocketPath() = %q, expected fallback path, not XDG_RUNTIME_DIR based", got)
	}
}

func TestEnsureDaemonDetectsRunningDaemon(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "looper.sock")

	s := daemon.New()
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- s.Serve(socketPath)
	}()
	t.Cleanup(func() {
		s.Stop()
		<-serveErrCh
	})

	// A bogus binary would fail to spawn if EnsureDaemon attempted it, so a
	// nil error here proves the "already running" path was taken.
	if err := EnsureDaemon(socketPath, "/nonexistent/looper-binary-that-does-not-exist"); err != nil {
		t.Fatalf("EnsureDaemon() with already-running daemon: %v", err)
	}
}
