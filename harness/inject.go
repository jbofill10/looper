package harness

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jbofill10/looper/config"
)

// hookCommand is one entry in Claude Code's hooks settings shape.
type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// hookGroup wraps a list of hookCommands, matching Claude Code's nested
// hooks settings shape.
type hookGroup struct {
	Hooks []hookCommand `json:"hooks"`
}

// hookSettings is the top-level Claude Code settings document controlling
// which commands run on which hook events.
type hookSettings struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

// hookEvents are the Claude Code hook events looper wires into every
// interactive session.
var hookEvents = []string{"PreToolUse", "PostToolUse", "Notification", "Stop"}

// WriteHookSettings writes a Claude Code settings file to path that wires
// PreToolUse, PostToolUse, Notification, and Stop hooks to invoke
// `<looperBin> hook --socket <socketPath>`, forwarding each hook's payload to
// the session's Unix socket listener.
func WriteHookSettings(path, looperBin, socketPath string) error {
	cmd := fmt.Sprintf("%q hook --socket %q", looperBin, socketPath)
	groups := []hookGroup{{Hooks: []hookCommand{{Type: "command", Command: cmd}}}}

	settings := hookSettings{Hooks: map[string][]hookGroup{}}
	for _, event := range hookEvents {
		settings.Hooks[event] = groups
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hook settings: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write hook settings %s: %w", path, err)
	}
	return nil
}

// BuildInteractive returns h.Interactive with "--settings", settingsPath, and
// prompt appended, forming the argv for an interactive harness session. It
// errors if h.Interactive is empty.
func BuildInteractive(h config.Harness, prompt, settingsPath string) ([]string, error) {
	if len(h.Interactive) == 0 {
		return nil, fmt.Errorf("harness has no interactive command configured")
	}
	argv := make([]string, len(h.Interactive), len(h.Interactive)+3)
	copy(argv, h.Interactive)
	argv = append(argv, "--settings", settingsPath, prompt)
	return argv, nil
}
