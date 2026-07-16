// Package draft launches a one-off, human-attended interactive harness
// session used to draft a script step's contents from inside the guided
// loop builder. It is deliberately independent of the state-machine
// machinery in runner.InteractiveExecutor (hook sockets, sentinel-derived
// session state): a draft session is just "run the harness here with this
// prompt, let the human collaborate with it, then read back what it
// wrote."
package draft

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/harness"
	looperpty "github.com/jbofill10/looper/pty"
)

// Request carries the preliminary context available when the user asks to
// draft a script step's contents.
type Request struct {
	LoopName   string
	StepName   string
	PriorSteps []config.Step
}

// Run ensures the loop-creation skill is installed in projectDir, starts
// an interactive session of h in projectDir with a prompt asking the
// harness to write the drafted script to a scratch file, attaches the
// local terminal to it until it exits, then reads back and returns the
// scratch file's contents.
func Run(projectDir string, h config.Harness, req Request) (string, error) {
	if len(h.Interactive) == 0 {
		return "", fmt.Errorf("harness has no interactive command configured")
	}

	skillPath, err := harness.EnsureLoopCreationSkill(projectDir)
	if err != nil {
		return "", fmt.Errorf("install loop-creation skill: %w", err)
	}

	scratchDir := filepath.Join(projectDir, ".looper", "tmp")
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		return "", fmt.Errorf("create scratch directory: %w", err)
	}
	scratch, err := os.CreateTemp(scratchDir, "draft-*.sh")
	if err != nil {
		return "", fmt.Errorf("create scratch file: %w", err)
	}
	scratchPath := scratch.Name()
	scratch.Close()
	defer os.Remove(scratchPath)

	prompt := buildPrompt(req, skillPath, scratchPath)

	argv := make([]string, len(h.Interactive), len(h.Interactive)+1)
	copy(argv, h.Interactive)
	argv = append(argv, prompt)

	sess, err := looperpty.Start(looperpty.Config{Argv: argv, Env: os.Environ(), Dir: projectDir})
	if err != nil {
		return "", fmt.Errorf("start draft session: %w", err)
	}

	runErr := sess.RunAttached(os.Stdin, os.Stdout)
	if runErr != nil {
		return "", fmt.Errorf("draft session: %w", runErr)
	}

	content, err := os.ReadFile(scratchPath)
	if err != nil {
		return "", fmt.Errorf("read drafted script: %w", err)
	}
	if strings.TrimSpace(string(content)) == "" {
		return "", fmt.Errorf("harness session exited without writing a script to %s", scratchPath)
	}
	return string(content), nil
}

// buildPrompt assembles the prompt handed to the harness: the skill to
// use, the loop/step being drafted, a summary of steps already defined
// (so the harness knows what environment/outputs already exist), and the
// scratch file it must write the drafted script to.
func buildPrompt(req Request, skillPath, scratchPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Use the loop-creation skill (%s) to help draft a looper script step.\n\n", skillPath)
	fmt.Fprintf(&b, "Loop: %s\nStep being drafted: %s (type: script)\n", req.LoopName, req.StepName)

	if len(req.PriorSteps) > 0 {
		b.WriteString("\nSteps already defined before this one:\n")
		for _, s := range req.PriorSteps {
			fmt.Fprintf(&b, "- %s (%s)\n", s.Name, s.Type)
		}
	}

	fmt.Fprintf(&b, "\nWrite the drafted shell script to %s and then stop; do not run it.\n", scratchPath)
	return b.String()
}
