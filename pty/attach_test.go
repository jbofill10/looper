package pty

import (
	"io"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestAttach_NonTTY_RoutesAndDetaches exercises Attach's routing loop without
// a controlling terminal: in is the read end of a pipe, which is not a tty,
// so the raw-mode branch (term.MakeRaw) must be skipped rather than erroring.
// It writes a few plain bytes followed by the Ctrl-\ d detach sequence and
// asserts Attach returns nil once detached.
func TestAttach_NonTTY_RoutesAndDetaches(t *testing.T) {
	s, err := Start(Config{Argv: []string{"sh", "-c", "cat"}})
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
		done <- s.Attach(inR, outW)
	}()

	if _, err := inW.Write([]byte("hello")); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	if _, err := inW.Write([]byte{0x1c, 'd'}); err != nil {
		t.Fatalf("write detach sequence: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Attach returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Attach did not return after detach sequence")
	}
}

// TestAttach_JoinsStdinReaderOnReturn guards against a regression where
// Attach's internal stdin-reading goroutine outlives Attach itself: if a
// caller reclaims in for its own use right after Attach returns (as the TUI
// does when it restores Bubble Tea's own stdin reader after a draft/attach
// session ends), a leaked goroutine still blocked in a Read on the same fd
// silently competes for keystrokes. Attach must join that goroutine before
// returning, so the goroutine count should settle back to (at most) its
// pre-Attach baseline shortly after Attach returns.
func TestAttach_JoinsStdinReaderOnReturn(t *testing.T) {
	s, err := Start(Config{Argv: []string{"sh", "-c", "true"}})
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

	before := runtime.NumGoroutine()

	if err := s.Attach(inR, outW); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if runtime.NumGoroutine() <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stdin-reading goroutine still running %v after Attach returned (before=%d, after=%d)",
				2*time.Second, before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
