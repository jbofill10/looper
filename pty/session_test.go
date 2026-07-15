package pty

import (
	"bytes"
	"strings"
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
