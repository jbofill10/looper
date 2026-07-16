package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureLoopCreationSkill_WritesUnderClaudeSkillsDir(t *testing.T) {
	dir := t.TempDir()

	path, err := EnsureLoopCreationSkill(dir)
	if err != nil {
		t.Fatalf("EnsureLoopCreationSkill: %v", err)
	}

	want := filepath.Join(dir, ".claude", "skills", "loop-creation", "SKILL.md")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written skill file: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "---\nname: loop-creation") {
		t.Errorf("skill file missing expected frontmatter, got:\n%s", content[:min(80, len(content))])
	}
	if !strings.Contains(content, "looper") {
		t.Errorf("skill file doesn't mention looper")
	}
}

func TestEnsureLoopCreationSkill_OverwritesStaleContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "skills", "loop-creation", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureLoopCreationSkill(dir); err != nil {
		t.Fatalf("EnsureLoopCreationSkill: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) == "stale" {
		t.Errorf("expected the stale skill file to be refreshed")
	}
}
