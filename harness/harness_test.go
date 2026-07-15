package harness

import (
	"testing"

	"github.com/jbofill10/looper/config"
)

func TestInterpolate_ReplacesKnownVars(t *testing.T) {
	got := Interpolate("plan {{TASK_ID}} end {{SENTINEL_DONE}}", map[string]string{
		"TASK_ID":       "42",
		"SENTINEL_DONE": "@@D@@",
	})
	want := "plan 42 end @@D@@"
	if got != want {
		t.Errorf("Interpolate = %q, want %q", got, want)
	}
}

func TestInterpolate_LeavesUnknownVarsLiteral(t *testing.T) {
	got := Interpolate("hi {{UNKNOWN}}", map[string]string{})
	want := "hi {{UNKNOWN}}"
	if got != want {
		t.Errorf("Interpolate = %q, want %q", got, want)
	}
}

func TestBuildHeadless_ReplacesPrompt(t *testing.T) {
	h := config.Harness{Headless: []string{"claude", "-p", "{{PROMPT}}"}}
	argv, err := BuildHeadless(h, "hi")
	if err != nil {
		t.Fatalf("BuildHeadless: %v", err)
	}
	want := []string{"claude", "-p", "hi"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	for i, v := range want {
		if argv[i] != v {
			t.Errorf("argv[%d] = %q, want %q", i, argv[i], v)
		}
	}
}

func TestBuildHeadless_EmptyHeadlessErrors(t *testing.T) {
	h := config.Harness{}
	if _, err := BuildHeadless(h, "hi"); err == nil {
		t.Errorf("expected error for empty Headless")
	}
}

func TestSentinelVars(t *testing.T) {
	h := config.Harness{Sentinels: config.Sentinels{
		NeedsInput: "NI",
		Done:       "D",
		NoWork:     "NW",
	}}
	vars := SentinelVars(h)
	if vars["SENTINEL_NEEDS_INPUT"] != "NI" {
		t.Errorf("SENTINEL_NEEDS_INPUT = %q", vars["SENTINEL_NEEDS_INPUT"])
	}
	if vars["SENTINEL_DONE"] != "D" {
		t.Errorf("SENTINEL_DONE = %q", vars["SENTINEL_DONE"])
	}
	if vars["SENTINEL_NO_WORK"] != "NW" {
		t.Errorf("SENTINEL_NO_WORK = %q", vars["SENTINEL_NO_WORK"])
	}
}
