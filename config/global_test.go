package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadGlobal_NonexistentPathReturnsDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	g, err := LoadGlobal(p)
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if g.DefaultHarness != "claude" {
		t.Errorf("DefaultHarness = %q, want claude", g.DefaultHarness)
	}
	if _, ok := g.Harnesses["claude"]; !ok {
		t.Errorf("expected claude harness present")
	}
}

func TestGlobal_HarnessNamesSorted(t *testing.T) {
	g := &Global{Harnesses: map[string]Harness{
		"gemini": {}, "claude": {}, "aider": {},
	}}
	got := g.HarnessNames()
	want := []string{"aider", "claude", "gemini"}
	if len(got) != len(want) {
		t.Fatalf("HarnessNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("HarnessNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDefaultGlobal_ResolveHarnessEmptyName(t *testing.T) {
	g := DefaultGlobal()
	h, err := g.ResolveHarness("")
	if err != nil {
		t.Fatalf("ResolveHarness: %v", err)
	}
	want := []string{"claude", "-p", "{{PROMPT}}"}
	if len(h.Headless) != len(want) {
		t.Fatalf("Headless = %v, want %v", h.Headless, want)
	}
	for i, v := range want {
		if h.Headless[i] != v {
			t.Errorf("Headless[%d] = %q, want %q", i, h.Headless[i], v)
		}
	}
}

func TestLoadGlobal_CustomHarnessMergesDefault(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := `
default_harness: foo
harnesses:
  foo:
    headless: ["foo", "-p", "{{PROMPT}}"]
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	g, err := LoadGlobal(p)
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if g.DefaultHarness != "foo" {
		t.Errorf("DefaultHarness = %q, want foo", g.DefaultHarness)
	}
	if _, ok := g.Harnesses["foo"]; !ok {
		t.Errorf("expected foo harness present")
	}
	if _, ok := g.Harnesses["claude"]; !ok {
		t.Errorf("expected claude harness still present (merged default)")
	}
}

func TestResolveHarness_UnknownErrors(t *testing.T) {
	g := DefaultGlobal()
	if _, err := g.ResolveHarness("nope"); err == nil {
		t.Errorf("expected error for unknown harness")
	}
}
