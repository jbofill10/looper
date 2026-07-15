package runner

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runctx"
)

// ScriptExecutor runs a shell command for a script step.
type ScriptExecutor struct {
	Prompter Prompter // consulted only for on_fail=ask
}

// Run executes step.Run via `sh -c` in the WORKDIR, with the run context vars
// injected as environment plus LOOPER_OUTPUT pointing at the step's outputs
// file. stdout+stderr are captured to steps/<name>.log.
func (e *ScriptExecutor) Run(rc *runctx.RunContext, step config.Step) (Outcome, error) {
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

	cmd := exec.Command("sh", "-c", step.Run)
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
			return 0, fmt.Errorf("run script %q: %w", step.Name, runErr)
		}
		exitCode = ee.ExitCode()
	}

	// Capture declared outputs regardless of exit code.
	if len(step.Outputs) > 0 {
		if err := captureOutputs(rc, step, outPath); err != nil {
			return 0, err
		}
	}

	if exitCode == 0 {
		return OutcomeAdvance, nil
	}
	if step.SignalsNoWork && exitCode == NoWorkExitCode {
		return OutcomeNoWork, nil
	}
	return resolveFailure(e.Prompter, step, exitCode)
}

// resolveFailure decides the outcome of a failed script/headless step based
// on its on_fail policy, consulting p only for OnFailAsk.
func resolveFailure(p Prompter, step config.Step, exitCode int) (Outcome, error) {
	switch step.OnFail {
	case config.OnFailRetry:
		return OutcomeRetry, nil
	case config.OnFailAbort:
		return OutcomeAbort, nil
	default: // OnFailAsk (validation defaults empty -> ask)
		if p == nil {
			return OutcomeAbort, nil
		}
		return p.AskFailure(step, exitCode)
	}
}

func captureOutputs(rc *runctx.RunContext, step config.Step, outPath string) error {
	f, err := os.Open(outPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open outputs: %w", err)
	}
	defer f.Close()

	declared := map[string]bool{}
	for _, k := range step.Outputs {
		declared[k] = true
	}
	found := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if declared[k] {
			found[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan outputs: %w", err)
	}
	for k, v := range found {
		rc.Set(k, v)
	}
	return nil
}

// asExitError reports whether err is an *exec.ExitError and, if so, assigns it.
func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}
