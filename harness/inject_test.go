package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jbofill10/looper/config"
)

type hookEntry struct {
	Hooks []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"hooks"`
}

type settingsDoc struct {
	Hooks map[string][]hookEntry `json:"hooks"`
}

func TestWriteHookSettings_WritesAllFourEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	socket := "/tmp/looper-x.sock"
	looperBin := "/usr/local/bin/looper"

	if err := WriteHookSettings(path, looperBin, socket); err != nil {
		t.Fatalf("WriteHookSettings: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var doc settingsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	for _, event := range []string{"PreToolUse", "PostToolUse", "Notification", "Stop"} {
		entries, ok := doc.Hooks[event]
		if !ok || len(entries) == 0 || len(entries[0].Hooks) == 0 {
			t.Fatalf("missing hook entry for %s", event)
		}
		cmd := entries[0].Hooks[0].Command
		if !strings.Contains(cmd, "hook --socket") {
			t.Errorf("%s command = %q, missing 'hook --socket'", event, cmd)
		}
		if !strings.Contains(cmd, socket) {
			t.Errorf("%s command = %q, missing socket path %q", event, cmd, socket)
		}
	}
}

func TestBuildInteractive_AppendsSettingsAndPrompt(t *testing.T) {
	h := config.Harness{Interactive: []string{"claude"}}
	argv, err := BuildInteractive(h, "do it", "/tmp/s.json")
	if err != nil {
		t.Fatalf("BuildInteractive: %v", err)
	}
	want := []string{"claude", "--settings", "/tmp/s.json", "do it"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	for i, v := range want {
		if argv[i] != v {
			t.Errorf("argv[%d] = %q, want %q", i, argv[i], v)
		}
	}
}

func TestBuildInteractive_EmptyInteractiveErrors(t *testing.T) {
	h := config.Harness{}
	if _, err := BuildInteractive(h, "do it", "/tmp/s.json"); err == nil {
		t.Errorf("expected error for empty Interactive")
	}
}
