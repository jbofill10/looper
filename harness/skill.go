package harness

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed skills/loop-creation/SKILL.md
var loopCreationSkill embed.FS

// EnsureLoopCreationSkill writes looper's bundled loop-creation Claude
// Skill to <projectDir>/.claude/skills/loop-creation/SKILL.md, creating
// parent directories as needed, and returns the path written. The file is
// looper-managed and always refreshed to the version bundled in the
// running binary, so a harness session started in projectDir (e.g. by
// InteractiveExecutor or a builder draft session) automatically discovers
// it as a project skill specialized in authoring looper loops/steps.
func EnsureLoopCreationSkill(projectDir string) (string, error) {
	data, err := loopCreationSkill.ReadFile("skills/loop-creation/SKILL.md")
	if err != nil {
		return "", fmt.Errorf("read embedded loop-creation skill: %w", err)
	}

	dir := filepath.Join(projectDir, ".claude", "skills", "loop-creation")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create skill directory: %w", err)
	}

	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write skill file: %w", err)
	}
	return path, nil
}
