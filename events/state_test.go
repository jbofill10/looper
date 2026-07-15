package events

import (
	"testing"

	"github.com/jbofill10/looper/config"
)

func sentinels() config.Sentinels {
	return config.Sentinels{
		NeedsInput: "@@NEEDS_INPUT@@",
		Done:       "@@DONE@@",
		NoWork:     "@@NO_WORK@@",
	}
}

func TestDerive(t *testing.T) {
	s := sentinels()
	cases := []struct {
		name string
		prev State
		hook Hook
		want State
	}{
		{
			name: "PreToolUse -> working",
			prev: StateStarting,
			hook: Hook{EventName: "PreToolUse"},
			want: StateWorking,
		},
		{
			name: "PostToolUse -> working",
			prev: StateStarting,
			hook: Hook{EventName: "PostToolUse"},
			want: StateWorking,
		},
		{
			name: "Notification permission_prompt -> needs_human",
			prev: StateWorking,
			hook: Hook{EventName: "Notification", NotificationType: "permission_prompt"},
			want: StateNeedsHuman,
		},
		{
			name: "Notification other type -> unchanged",
			prev: StateWorking,
			hook: Hook{EventName: "Notification", NotificationType: "idle"},
			want: StateWorking,
		},
		{
			name: "Stop with NoWork sentinel -> no_work",
			prev: StateWorking,
			hook: Hook{EventName: "Stop", LastAssistantMessage: "done here: @@NO_WORK@@"},
			want: StateNoWork,
		},
		{
			name: "Stop with NeedsInput sentinel -> needs_human",
			prev: StateWorking,
			hook: Hook{EventName: "Stop", LastAssistantMessage: "hey @@NEEDS_INPUT@@ please"},
			want: StateNeedsHuman,
		},
		{
			name: "Stop with Done sentinel -> awaiting_approval",
			prev: StateWorking,
			hook: Hook{EventName: "Stop", LastAssistantMessage: "all set @@DONE@@"},
			want: StateAwaitingApproval,
		},
		{
			name: "Stop with no sentinel -> awaiting_input",
			prev: StateWorking,
			hook: Hook{EventName: "Stop", LastAssistantMessage: "just some text"},
			want: StateAwaitingInput,
		},
		{
			name: "unknown event -> prev unchanged",
			prev: StateNeedsHuman,
			hook: Hook{EventName: "SomethingElse"},
			want: StateNeedsHuman,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Derive(tc.prev, tc.hook, s)
			if got != tc.want {
				t.Errorf("Derive(%v, %+v) = %v, want %v", tc.prev, tc.hook, got, tc.want)
			}
		})
	}
}
