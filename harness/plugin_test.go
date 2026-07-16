package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureStepAuthoringPlugin_WritesManifestAndSkill(t *testing.T) {
	dir, err := EnsureStepAuthoringPlugin()
	if err != nil {
		t.Fatalf("EnsureStepAuthoringPlugin: %v", err)
	}

	manifestPath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	if !strings.Contains(string(data), `"name": "step-authoring"`) {
		t.Errorf("manifest missing expected name field, got:\n%s", data)
	}

	skillPath := filepath.Join(dir, "skills", "step-authoring", "SKILL.md")
	skillData, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("reading skill file: %v", err)
	}
	if !strings.HasPrefix(string(skillData), "---\nname: step-authoring") {
		t.Errorf("skill file missing expected frontmatter, got:\n%s", skillData[:min(80, len(skillData))])
	}
}

func TestEnsureStepAuthoringPlugin_OverwritesStaleContent(t *testing.T) {
	dir, err := EnsureStepAuthoringPlugin()
	if err != nil {
		t.Fatalf("EnsureStepAuthoringPlugin: %v", err)
	}
	manifestPath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	if err := os.WriteFile(manifestPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureStepAuthoringPlugin(); err != nil {
		t.Fatalf("EnsureStepAuthoringPlugin: %v", err)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) == "stale" {
		t.Errorf("expected the stale manifest to be refreshed")
	}
}
