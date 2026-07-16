// Package harness builds prompt text and headless command lines for
// configured agentic-coding harnesses (e.g. claude -p).
package harness

import (
	"fmt"
	"strings"

	"github.com/jbofill10/looper/config"
)

// Interpolate replaces every "{{KEY}}" occurrence in s with vars[KEY].
// Keys not present in vars are left untouched (literal "{{KEY}}").
func Interpolate(s string, vars map[string]string) string {
	var b strings.Builder
	for {
		start := strings.Index(s, "{{")
		if start == -1 {
			b.WriteString(s)
			break
		}
		end := strings.Index(s[start:], "}}")
		if end == -1 {
			b.WriteString(s)
			break
		}
		end += start
		key := s[start+2 : end]
		b.WriteString(s[:start])
		if v, ok := vars[key]; ok {
			b.WriteString(v)
		} else {
			b.WriteString("{{" + key + "}}")
		}
		s = s[end+2:]
	}
	return b.String()
}

// SentinelVars returns the sentinel strings of h as an interpolation vars map
// keyed SENTINEL_NEEDS_INPUT / SENTINEL_DONE / SENTINEL_NO_WORK.
func SentinelVars(h config.Harness) map[string]string {
	return map[string]string{
		"SENTINEL_NEEDS_INPUT": h.Sentinels.NeedsInput,
		"SENTINEL_DONE":        h.Sentinels.Done,
		"SENTINEL_NO_WORK":     h.Sentinels.NoWork,
	}
}

// BuildHeadless returns a copy of h.Headless with every "{{PROMPT}}" token
// replaced by prompt. It errors if h.Headless is empty.
func BuildHeadless(h config.Harness, prompt string) ([]string, error) {
	if len(h.Headless) == 0 {
		return nil, fmt.Errorf("harness has no headless command configured")
	}
	argv := make([]string, len(h.Headless))
	vars := map[string]string{"PROMPT": prompt}
	for i, tok := range h.Headless {
		argv[i] = Interpolate(tok, vars)
	}
	return argv, nil
}

// BuildStepAuthoring returns h.Interactive with "--plugin-dir", pluginDir,
// and prompt appended, forming the argv for a step-authoring session
// (see stepauthor.CreateStep/EditStep). Unlike BuildInteractive, no
// --settings is involved: --plugin-dir alone activates a local plugin for
// the session. It errors if h.Interactive is empty.
func BuildStepAuthoring(h config.Harness, prompt, pluginDir string) ([]string, error) {
	if len(h.Interactive) == 0 {
		return nil, fmt.Errorf("harness has no interactive command configured")
	}
	argv := make([]string, len(h.Interactive), len(h.Interactive)+3)
	copy(argv, h.Interactive)
	argv = append(argv, "--plugin-dir", pluginDir, prompt)
	return argv, nil
}
