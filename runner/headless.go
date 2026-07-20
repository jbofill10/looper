package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/harness"
	"github.com/jbofill10/looper/runctx"
)

// HeadlessExecutor runs a step's prompt through a configured harness's
// headless command (e.g. `claude -p <prompt>`).
type HeadlessExecutor struct {
	Harness  config.Harness
	Prompter Prompter // consulted only for on_fail=ask
}

// Run interpolates step.Prompt with the run context vars and sentinel vars,
// builds the harness's headless argv, and executes it in the WORKDIR, with
// LOOPER_OUTPUT pointing at the step's outputs file. stdout+stderr are
// captured to steps/<name>.log.
func (e *HeadlessExecutor) Run(rc *runctx.RunContext, step config.Step) (Outcome, error) {
	vars := map[string]string{}
	for k, v := range rc.Vars {
		vars[k] = v
	}
	for k, v := range harness.SentinelVars(e.Harness) {
		vars[k] = v
	}
	prompt := harness.Interpolate(step.Prompt, vars)

	argv, err := harness.BuildHeadless(e.Harness, prompt)
	if err != nil {
		return 0, fmt.Errorf("build headless command for step %q: %w", step.Name, err)
	}

	outPath := filepath.Join(rc.StepsDir(), step.Name+".outputs")
	logPath := filepath.Join(rc.StepsDir(), step.Name+".log")

	// Truncate any prior outputs file so a retry starts clean.
	if err := os.WriteFile(outPath, nil, 0o644); err != nil {
		return 0, fmt.Errorf("init outputs file: %w", err)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return 0, fmt.Errorf("create log: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(argv[0], argv[1:]...)
	if wd, ok := rc.Get("WORKDIR"); ok {
		cmd.Dir = wd
	}
	cmd.Env = append(os.Environ(), rc.Env()...)
	cmd.Env = append(cmd.Env, "LOOPER_OUTPUT="+outPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if !asExitError(runErr, &ee) {
			return 0, fmt.Errorf("run headless step %q: %w", step.Name, runErr)
		}
		exitCode = ee.ExitCode()
	}

	// Capture declared outputs regardless of exit code.
	if len(step.Outputs) > 0 || step.Digest != "" {
		if err := captureOutputs(rc, step, outPath); err != nil {
			return 0, err
		}
	}
	if err := captureDigest(rc, step); err != nil {
		return 0, err
	}

	if exitCode == 0 {
		return OutcomeAdvance, nil
	}
	if step.SignalsNoWork && exitCode == NoWorkExitCode {
		return OutcomeNoWork, nil
	}
	return resolveFailure(e.Prompter, step, exitCode)
}
