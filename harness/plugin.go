package harness

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// StepAuthoringPluginName is the plugin's name, as declared in
// plugin/.claude-plugin/plugin.json — the value a --plugin-dir-loaded
// session's skill is registered under.
const StepAuthoringPluginName = "step-authoring"

//go:embed plugin/.claude-plugin/plugin.json plugin/skills/step-authoring/SKILL.md
var stepAuthoringPlugin embed.FS

// stepAuthoringPluginFiles maps each embedded file to its path relative
// to the plugin directory root.
var stepAuthoringPluginFiles = map[string]string{
	"plugin/.claude-plugin/plugin.json":     filepath.Join(".claude-plugin", "plugin.json"),
	"plugin/skills/step-authoring/SKILL.md": filepath.Join("skills", "step-authoring", "SKILL.md"),
}

// EnsureStepAuthoringPlugin extracts looper's bundled step-authoring
// Claude Code plugin to a looper-owned cache directory (never inside the
// user's project, so ordinary claude sessions never discover it),
// refreshing it on every call, and returns the plugin directory written.
// A --plugin-dir-loaded session pointed at this directory activates the
// plugin for that session alone; nothing needs to touch enabledPlugins.
func EnsureStepAuthoringPlugin() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	dir := filepath.Join(cacheDir, "looper", "plugin")

	for embedPath, rel := range stepAuthoringPluginFiles {
		data, err := stepAuthoringPlugin.ReadFile(embedPath)
		if err != nil {
			return "", fmt.Errorf("read embedded %s: %w", embedPath, err)
		}
		dest := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", fmt.Errorf("create plugin directory: %w", err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", dest, err)
		}
	}
	return dir, nil
}
