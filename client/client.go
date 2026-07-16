// Package client provides a dial helper and daemon auto-spawn logic for
// talking to looperd over its Unix socket.
package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jbofill10/looper/rpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// pingTimeout bounds a single Ping RPC used to probe daemon liveness.
const pingTimeout = 500 * time.Millisecond

// ensureDaemonTimeout bounds how long EnsureDaemon waits for a newly spawned
// daemon to become reachable.
const ensureDaemonTimeout = 3 * time.Second

// SocketPath returns the default Unix socket path looper uses to talk to
// looperd: $XDG_RUNTIME_DIR/looper.sock if XDG_RUNTIME_DIR is set, otherwise
// a per-user path under os.TempDir().
func SocketPath() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "looper.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("looper-%d.sock", os.Getuid()))
}

// Dial connects to looperd listening on the Unix socket at socketPath. The
// caller is responsible for closing the returned connection.
func Dial(socketPath string) (rpc.LooperClient, *grpc.ClientConn, error) {
	abs, err := filepath.Abs(socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving socket path %s: %w", socketPath, err)
	}
	conn, err := grpc.NewClient("unix://"+abs, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dialing %s: %w", abs, err)
	}
	return rpc.NewLooperClient(conn), conn, nil
}

// ping performs a single bounded-timeout Ping RPC against the daemon at
// socketPath, returning an error if it is unreachable or does not respond.
func ping(socketPath string) error {
	client, conn, err := Dial(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	_, err = client.Ping(ctx, &rpc.PingRequest{})
	return err
}

// EnsureDaemon makes sure looperd is running and reachable at socketPath. If
// it is already running, EnsureDaemon returns immediately. Otherwise it
// spawns looperBin as a detached "daemon" process and polls until it
// responds to Ping, bounded by ensureDaemonTimeout.
func EnsureDaemon(socketPath, looperBin string) error {
	if err := ping(socketPath); err == nil {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return fmt.Errorf("creating socket directory: %w", err)
	}

	logPath := socketPath + ".log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening daemon log file %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(looperBin, "daemon", "--socket", socketPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawning daemon %s: %w", looperBin, err)
	}

	deadline := time.Now().Add(ensureDaemonTimeout)
	for time.Now().Before(deadline) {
		if err := ping(socketPath); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become reachable at %s within %s", socketPath, ensureDaemonTimeout)
}
