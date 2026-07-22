package pty

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitForScrollback spin-waits (bounded) until Scrollback contains substr,
// failing the test if it doesn't appear in time. This is a test-only
// polling loop, not a sleep used to mask a race.
func waitForScrollback(t *testing.T, s *Session, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains(s.Scrollback(), []byte(substr)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("scrollback never contained %q; got %q", substr, s.Scrollback())
}

func TestSession_WriteAndCapture(t *testing.T) {
	s, err := Start(Config{Argv: []string{"sh", "-c", `printf hello; read line; printf "got:%s" "$line"`}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	waitForScrollback(t, s, "hello")

	if _, err := s.Write([]byte("world\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if !strings.Contains(string(s.Scrollback()), "got:world") {
		t.Errorf("scrollback = %q, want it to contain %q", s.Scrollback(), "got:world")
	}
}

func TestSession_ScrollbackCap(t *testing.T) {
	const cap = 1024
	s, err := Start(Config{
		Argv:            []string{"sh", "-c", "yes x | head -c 4096"},
		ScrollbackBytes: cap,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if got := len(s.Scrollback()); got > cap {
		t.Errorf("len(Scrollback()) = %d, want <= %d", got, cap)
	}
}

// TestSession_RunAttached_DetachWhileRunningKillsSession guards against a
// regression where a one-shot local-attach caller (draft.Run,
// runner.runPTY) blocks in Wait() forever after the human detaches while the
// child is still alive: since these callers have no way to reattach later,
// RunAttached must treat that detach as abandonment and kill the session
// rather than leaving it (and the caller) waiting indefinitely.
func TestSession_RunAttached_DetachWhileRunningKillsSession(t *testing.T) {
	s, err := Start(Config{Argv: []string{"sh", "-c", "sleep 30"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe (in): %v", err)
	}
	defer inR.Close()
	defer inW.Close()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe (out): %v", err)
	}
	defer outR.Close()
	defer outW.Close()
	go io.Copy(io.Discard, outR)

	done := make(chan error, 1)
	go func() {
		done <- s.RunAttached(inR, outW)
	}()

	// Give RunAttached/Attach a moment to reach their select before sending
	// the detach escape, so it's actually observed as a mid-session detach
	// rather than racing session startup.
	time.Sleep(100 * time.Millisecond)
	if _, err := inW.Write([]byte{0x1c, 'd'}); err != nil {
		t.Fatalf("write detach sequence: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Errorf("RunAttached returned nil error; want a non-nil error from killing the still-running sleep 30 child")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunAttached did not return after detach while the child was still running; it is left waiting on an abandoned session")
	}
}

func TestSession_Resize(t *testing.T) {
	s, err := Start(Config{Argv: []string{"sh", "-c", "sleep 1"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if err := s.Resize(40, 120); err != nil {
		t.Errorf("Resize: %v", err)
	}
}

func TestSession_CloseTwiceDoesNotPanic(t *testing.T) {
	s, err := Start(Config{Argv: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close (1st): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close (2nd): %v", err)
	}
}

func TestSession_WaitAfterNormalExitReturnsNil(t *testing.T) {
	s, err := Start(Config{Argv: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if err := s.Wait(); err != nil {
		t.Errorf("Wait: %v", err)
	}
}

// syncBuffer is a *bytes.Buffer guarded by a mutex, safe for concurrent
// Write (from the session's reader goroutine) and Read/String (from the
// test goroutine).
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

func waitForSyncBuf(t *testing.T, buf *syncBuffer, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), substr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("buffer never contained %q; got %q", substr, buf.String())
}

func TestSession_PipeToStreamsLiveOutput(t *testing.T) {
	s, err := Start(Config{Argv: []string{"cat"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	buf := &syncBuffer{}
	stop := s.PipeTo(buf)

	if _, err := s.Write([]byte("ping\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForSyncBuf(t, buf, "ping")

	stop()

	// Further output must not reach buf once stopped. Since stop() is
	// idempotent and there's no direct signal for "will never arrive",
	// snapshot the buffer, write more input, and assert it doesn't grow.
	before := buf.String()
	if _, err := s.Write([]byte("pong\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForScrollback(t, s, "pong")
	if got := buf.String(); got != before {
		t.Errorf("buf grew after stop(): before=%q after=%q", before, got)
	}

	stop() // idempotent
}

func TestSession_PipeToReplaysScrollback(t *testing.T) {
	s, err := Start(Config{Argv: []string{"cat"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if _, err := s.Write([]byte("before\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForScrollback(t, s, "before")

	buf := &syncBuffer{}
	stop := s.PipeTo(buf)
	defer stop()

	if !strings.Contains(buf.String(), "before") {
		t.Errorf("PipeTo did not replay scrollback; buf = %q", buf.String())
	}
}
