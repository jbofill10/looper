// Package events listens for Claude Code hook callbacks over a Unix socket
// and derives interactive-session state from them.
package events

import (
	"strings"

	"github.com/jbofill10/looper/config"
)

// Hook is the JSON payload Claude Code sends to a hook command's stdin.
type Hook struct {
	EventName            string `json:"hook_event_name"`
	NotificationType     string `json:"notification_type"`
	ToolName             string `json:"tool_name"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// State is the derived state of an interactive session.
type State string

const (
	StateStarting         State = "starting"
	StateWorking          State = "working"
	StateNeedsHuman       State = "needs_human"
	StateAwaitingApproval State = "awaiting_approval"
	StateNoWork           State = "no_work"
	StateAwaitingInput    State = "awaiting_input"
)

// Derive computes the next session state given the previous state and an
// incoming hook event, consulting s to recognize sentinel strings embedded in
// a Stop hook's last assistant message. It is a pure function: unrecognized
// events leave prev unchanged.
func Derive(prev State, h Hook, s config.Sentinels) State {
	switch h.EventName {
	case "PreToolUse", "PostToolUse":
		return StateWorking
	case "Notification":
		if h.NotificationType == "permission_prompt" {
			return StateNeedsHuman
		}
		return prev
	case "Stop":
		switch {
		case s.NoWork != "" && strings.Contains(h.LastAssistantMessage, s.NoWork):
			return StateNoWork
		case s.NeedsInput != "" && strings.Contains(h.LastAssistantMessage, s.NeedsInput):
			return StateNeedsHuman
		case s.Done != "" && strings.Contains(h.LastAssistantMessage, s.Done):
			return StateAwaitingApproval
		default:
			return StateAwaitingInput
		}
	default:
		return prev
	}
}
