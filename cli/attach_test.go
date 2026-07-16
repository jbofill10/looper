package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jbofill10/looper/client"
	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/daemon"
	"github.com/jbofill10/looper/rpc"
	"gopkg.in/yaml.v3"
)

// syncBuffer is a *bytes.Buffer guarded by a mutex, safe for a background
// io.Copy writer racing a polling reader.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// catGlobalForAttach returns a Global whose default harness's interactive
// command is `sh -c cat`, which echoes stdin to stdout, so this test doesn't
// need a real `claude` binary.
func catGlobalForAttach() *config.Global {
	return &config.Global{
		DefaultHarness: "catty",
		Harnesses: map[string]config.Harness{
			"catty": {Interactive: []string{"sh", "-c", "cat"}},
		},
	}
}

// TestAttach_NonTTYSmoke exercises `looper attach` end to end via the built
// binary, with stdin a pipe (not a terminal) so the raw-mode path (manual-
// only, as in M4) is skipped. It attaches to a live `cat` session, writes a
// line, closes stdin, and asserts the process streams the echoed output and
// exits cleanly. The daemon-side Attach bridge itself is already covered by
// daemon.TestService_AttachStreamsInputAndOutput; this only smoke-tests the
// client wiring.
func TestAttach_NonTTYSmoke(t *testing.T) {
	binPath := buildLooperBinary(t)

	socketPath := filepath.Join(t.TempDir(), "looper.sock")
	s := daemon.NewWithGlobal(catGlobalForAttach(), binPath)
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- s.Serve(socketPath)
	}()
	t.Cleanup(func() {
		s.Stop()
		select {
		case <-serveErrCh:
		case <-time.After(2 * time.Second):
			t.Errorf("timed out waiting for Serve to return during cleanup")
		}
	})
	waitForPing(t, socketPath, 2*time.Second)

	c, conn, err := client.Dial(socketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	dir := t.TempDir()
	loop := &config.Loop{
		Name:  "l",
		Steps: []config.Step{{Name: "sess", Type: config.StepInteractive, Prompt: "hi"}},
	}
	data, err := yaml.Marshal(loop)
	if err != nil {
		t.Fatalf("marshal loop: %v", err)
	}
	loopPath := filepath.Join(dir, "l.yaml")
	if err := os.WriteFile(loopPath, data, 0o644); err != nil {
		t.Fatalf("write loop file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startResp, err := c.StartLoop(ctx, &rpc.StartLoopRequest{
		LoopFile: loopPath, BaseDir: filepath.Join(dir, ".looper"), Workdir: dir,
	})
	if err != nil {
		t.Fatalf("StartLoop: %v", err)
	}

	stateStream, err := c.StreamState(ctx, &rpc.StreamStateRequest{RunId: startResp.RunId})
	if err != nil {
		t.Fatalf("StreamState: %v", err)
	}
	for {
		u, err := stateStream.Recv()
		if err != nil {
			t.Fatalf("StreamState.Recv: %v", err)
		}
		if u.Kind == "state" && u.State == "session_live" {
			break
		}
	}

	attachCmd := exec.Command(binPath, "attach", startResp.RunId, "--socket", socketPath)
	stdinPipe, err := attachCmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := attachCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	attachCmd.Stderr = &stderr

	if err := attachCmd.Start(); err != nil {
		t.Fatalf("starting attach: %v", err)
	}

	stdout := &syncBuffer{}
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stdout, stdoutPipe)
		close(copyDone)
	}()

	if _, err := stdinPipe.Write([]byte("hi\n")); err != nil {
		t.Fatalf("write to attach stdin: %v", err)
	}

	// Wait for the echoed output to actually arrive before closing stdin:
	// closing right after the write would race the pty round trip (fork,
	// read, echo) against the client's CloseSend-on-EOF, which can end the
	// stream before the echo is tapped and forwarded.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stdout.String(), "hi") {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for echoed output; got %q (stderr=%q)", stdout.String(), stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := stdinPipe.Close(); err != nil {
		t.Fatalf("close attach stdin: %v", err)
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- attachCmd.Wait() }()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("attach process exited with error: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = attachCmd.Process.Kill()
		t.Fatalf("attach process did not exit in time; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	<-copyDone

	if !strings.Contains(stdout.String(), "hi") {
		t.Errorf("attach stdout = %q, want it to contain %q", stdout.String(), "hi")
	}
}
