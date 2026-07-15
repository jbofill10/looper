package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jbofill10/looper/events"
)

var hookSocketCounter int64

func hookSocketPath(t *testing.T) string {
	t.Helper()
	n := atomic.AddInt64(&hookSocketCounter, 1)
	path := filepath.Join(os.TempDir(), "looper-hook-"+strconv.FormatInt(n, 10)+".sock")
	t.Cleanup(func() { os.Remove(path) })
	return path
}

func TestForwardHook_DeliversToListener(t *testing.T) {
	path := hookSocketPath(t)
	l, err := events.Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	hookJSON := `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`
	if err := forwardHook(strings.NewReader(hookJSON), path); err != nil {
		t.Fatalf("forwardHook: %v", err)
	}

	select {
	case h := <-l.Events():
		if h.EventName != "PreToolUse" || h.ToolName != "Bash" {
			t.Errorf("got %+v", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestForwardHook_NonexistentSocketErrors(t *testing.T) {
	path := filepath.Join(os.TempDir(), "looper-hook-nonexistent.sock")
	if err := forwardHook(strings.NewReader(`{}`), path); err == nil {
		t.Fatal("expected error dialing nonexistent socket")
	}
}
