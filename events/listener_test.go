package events

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// testSocketPath returns a short, deterministic socket path under
// os.TempDir(), avoiding the ~104-byte unix socket path limit that
// t.TempDir()-based paths can exceed.
var socketCounter int64

func testSocketPath(t *testing.T) string {
	t.Helper()
	n := atomic.AddInt64(&socketCounter, 1)
	path := filepath.Join(os.TempDir(), "looper-"+strconv.FormatInt(n, 10)+".sock")
	t.Cleanup(func() { os.Remove(path) })
	return path
}

func dialAndSend(t *testing.T, path string, h Hook) {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close conn: %v", err)
	}
}

func TestListen_SingleEvent(t *testing.T) {
	path := testSocketPath(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	dialAndSend(t, path, Hook{EventName: "PreToolUse", ToolName: "Bash"})

	select {
	case h := <-l.Events():
		if h.EventName != "PreToolUse" || h.ToolName != "Bash" {
			t.Errorf("got %+v", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestListen_TwoSequentialConnections(t *testing.T) {
	path := testSocketPath(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	dialAndSend(t, path, Hook{EventName: "PreToolUse"})
	first := <-l.Events()
	dialAndSend(t, path, Hook{EventName: "PostToolUse"})
	second := <-l.Events()

	if first.EventName != "PreToolUse" {
		t.Errorf("first = %+v", first)
	}
	if second.EventName != "PostToolUse" {
		t.Errorf("second = %+v", second)
	}
}

func TestListener_Path(t *testing.T) {
	path := testSocketPath(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()
	if l.Path() != path {
		t.Errorf("Path() = %q, want %q", l.Path(), path)
	}
}

func TestListener_CloseRemovesSocketAndClosesChannel(t *testing.T) {
	path := testSocketPath(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected socket file removed, stat err = %v", err)
	}
	// Ranging over the channel must terminate promptly.
	done := make(chan struct{})
	go func() {
		for range l.Events() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Events() channel did not close")
	}
}

func TestListener_CloseTwiceDoesNotPanic(t *testing.T) {
	path := testSocketPath(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
