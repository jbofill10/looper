// Package stepauthor launches a one-off, human-attended interactive
// claude session that creates or edits a step directly in a loop's YAML
// file on disk. Unlike runner.InteractiveExecutor, it has no hook socket
// or sentinel-derived state machine — it's just "run claude here, scoped
// to the step-authoring plugin, let the human collaborate with it, then
// wait for it to exit."
package stepauthor

import (
	"fmt"
	"os"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/harness"
	looperpty "github.com/jbofill10/looper/pty"
)

// CreateStep starts an interactive session of h in projectDir, prompting
// it to add a new step to the loop at loopPath, and attaches the local
// terminal to it until it exits.
func CreateStep(projectDir string, h config.Harness, loopPath string) error {
	prompt := fmt.Sprintf(
		"A new step needs to be added to the loop at %s. Ask the user what "+
			"the step should do, then use the step-authoring skill to add it "+
			"to the YAML correctly.", loopPath)
	return run(projectDir, h, prompt)
}

// EditStep starts an interactive session of h in projectDir, prompting it
// to edit the step named stepName in the loop at loopPath per the user's
// request. If validationErr is non-nil, its text is included so the
// session fixes that problem first.
func EditStep(projectDir string, h config.Harness, loopPath, stepName string, validationErr error) error {
	prompt := fmt.Sprintf(
		"Edit the step named %q in the loop at %s per the user's request. "+
			"Use the step-authoring skill.", stepName, loopPath)
	if validationErr != nil {
		prompt += fmt.Sprintf(
			" This step currently fails validation: %s. Fix that first, then "+
				"ask the user if there's anything else they want changed.",
			validationErr)
	}
	return run(projectDir, h, prompt)
}

// run ensures the step-authoring plugin is extracted, builds the session
// argv, starts it in projectDir, and attaches the local terminal to it
// until it exits.
func run(projectDir string, h config.Harness, prompt string) error {
	pluginDir, err := harness.EnsureStepAuthoringPlugin()
	if err != nil {
		return fmt.Errorf("install step-authoring plugin: %w", err)
	}

	argv, err := harness.BuildStepAuthoring(h, prompt, pluginDir)
	if err != nil {
		return err
	}

	sess, err := looperpty.Start(looperpty.Config{Argv: argv, Env: os.Environ(), Dir: projectDir})
	if err != nil {
		return fmt.Errorf("start step-authoring session: %w", err)
	}
	if err := sess.RunAttached(os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("step-authoring session: %w", err)
	}
	return nil
}
