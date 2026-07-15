package runner

import (
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/events"
	"github.com/jbofill10/looper/harness"
	looperpty "github.com/jbofill10/looper/pty"
	"github.com/jbofill10/looper/runctx"
)

// maxSocketPathLen is a conservative Unix domain socket path length limit
// (the actual sun_path limit is platform-dependent, commonly 104-108 bytes).
const maxSocketPathLen = 100

// InteractiveExecutor runs a step by handing the terminal to a harness's
// interactive command (e.g. `claude`) while listening on a per-session Unix
// socket for hook events that drive session-state derivation. The human has
// final say over the outcome via Prompter.Interactive.
type InteractiveExecutor struct {
	Harness   config.Harness
	Prompter  Prompter
	LooperBin string

	// run starts the session and blocks until it exits. Injectable for
	// tests; defaults to runPTY. socketPath is the listener socket; env
	// includes LOOPER_HOOK_SOCKET.
	run func(argv, env []string, socketPath string) error
}

// Run builds the interactive session's prompt, hook settings, and argv,
// starts the session (via run/runPTY), and derives the session's
// final state from the hook events received on the socket. The final
// outcome is StateNoWork -> OutcomeNoWork, otherwise whatever the Prompter
// decides after being told the final state.
func (e *InteractiveExecutor) Run(rc *runctx.RunContext, step config.Step) (Outcome, error) {
	socketPath := socketPathFor(rc, step)
	l, err := events.Listen(socketPath)
	if err != nil {
		return 0, fmt.Errorf("listen for interactive session %q: %w", step.Name, err)
	}
	defer l.Close()

	doneCh := make(chan events.State, 1)
	go func() {
		state := events.StateStarting
		for h := range l.Events() {
			state = events.Derive(state, h, e.Harness.Sentinels)
			_ = rc.AppendEvent(runctx.Event{Step: step.Name, Kind: "state", Message: string(state)})
		}
		doneCh <- state
	}()

	vars := map[string]string{}
	for k, v := range rc.Vars {
		vars[k] = v
	}
	for k, v := range harness.SentinelVars(e.Harness) {
		vars[k] = v
	}
	prompt := harness.Interpolate(step.Prompt, vars)

	settingsPath := filepath.Join(rc.StepsDir(), step.Name+"-settings.json")
	looperBin := e.LooperBin
	if looperBin == "" {
		looperBin = "looper"
	}
	if err := harness.WriteHookSettings(settingsPath, looperBin, socketPath); err != nil {
		return 0, fmt.Errorf("write hook settings for step %q: %w", step.Name, err)
	}

	argv, err := harness.BuildInteractive(e.Harness, prompt, settingsPath)
	if err != nil {
		return 0, fmt.Errorf("build interactive command for step %q: %w", step.Name, err)
	}

	outPath := filepath.Join(rc.StepsDir(), step.Name+".outputs")
	if err := os.WriteFile(outPath, nil, 0o644); err != nil {
		return 0, fmt.Errorf("init outputs file: %w", err)
	}

	env := append(os.Environ(), rc.Env()...)
	env = append(env, "LOOPER_HOOK_SOCKET="+socketPath, "LOOPER_OUTPUT="+outPath)

	run := e.run
	if run == nil {
		run = runPTY
	}
	runErr := run(argv, env, socketPath)

	// Close the listener now so the accept loop exits and the consumer
	// goroutine's done signal is delivered; the deferred Close is then a
	// no-op (idempotent).
	_ = l.Close()
	finalState := <-doneCh

	if runErr != nil {
		return 0, fmt.Errorf("run interactive step %q: %w", step.Name, runErr)
	}

	if len(step.Outputs) > 0 {
		if err := captureOutputs(rc, step, outPath); err != nil {
			return 0, err
		}
	}

	if finalState == events.StateNoWork {
		return OutcomeNoWork, nil
	}
	return e.Prompter.Interactive(step, string(finalState))
}

// socketPathFor picks a Unix socket path for step's session. It prefers a
// path under rc.StepsDir() for locality, but falls back to a short,
// deterministic name under os.TempDir() if the preferred path risks
// exceeding the platform's Unix socket path length limit.
func socketPathFor(rc *runctx.RunContext, step config.Step) string {
	preferred := filepath.Join(rc.StepsDir(), step.Name+".sock")
	if len(preferred) <= maxSocketPathLen {
		return preferred
	}
	sum := crc32.ChecksumIEEE([]byte(rc.Dir + "/" + step.Name))
	return filepath.Join(os.TempDir(), fmt.Sprintf("looper-%08x.sock", sum))
}

// runPTY is the default run implementation: it starts argv in a
// looper-owned pseudoterminal (cmd.Dir taken from the env's WORKDIR entry if
// present), auto-attaches the human to it, and waits for it to exit. It is
// intentionally thin and not unit-tested against a real pty in most cases;
// tests inject a fake run instead (see TestInteractive_DefaultRunUsesRealPTY
// for the one real-pty exception, skipped where a pty can't be allocated).
func runPTY(argv, env []string, socketPath string) error {
	var dir string
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "WORKDIR" {
			dir = v
			break
		}
	}

	sess, err := looperpty.Start(looperpty.Config{Argv: argv, Env: env, Dir: dir})
	if err != nil {
		return fmt.Errorf("start interactive pty: %w", err)
	}

	go func() {
		_ = sess.Attach(os.Stdin, os.Stdout)
	}()

	runErr := sess.Wait()
	_ = sess.Close()
	return runErr
}
