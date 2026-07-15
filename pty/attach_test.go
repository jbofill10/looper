package pty

import (
	"io"
	"os"
	"testing"
	"time"
)

// TestAttach_NonTTY_RoutesAndDetaches exercises Attach's routing loop without
// a controlling terminal: in is the read end of a pipe, which is not a tty,
// so the raw-mode branch (term.MakeRaw) must be skipped rather than erroring.
// It writes a few plain bytes followed by the Ctrl-b d detach sequence and
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
	if _, err := inW.Write([]byte{0x02, 'd'}); err != nil {
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
