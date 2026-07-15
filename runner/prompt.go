package runner

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/jbofill10/looper/config"
)

// StdinPrompter is the interactive terminal Prompter used by the CLI. It reads
// a single-letter choice: (a)dvance, (r)etry, (x)abort.
type StdinPrompter struct {
	In  io.Reader
	Out io.Writer
}

func (p *StdinPrompter) Manual(step config.Step) (Outcome, error) {
	fmt.Fprintf(p.Out, "Manual step %q. [a]dvance / [r]etry / [x]abort: ", step.Name)
	return p.readChoice()
}

func (p *StdinPrompter) AskFailure(step config.Step, exitCode int) (Outcome, error) {
	fmt.Fprintf(p.Out, "Step %q failed (exit %d). [a]dvance / [r]etry / [x]abort: ", step.Name, exitCode)
	return p.readChoice()
}

// Interactive asks the human to confirm the outcome of an interactive
// session that ended in finalState.
func (p *StdinPrompter) Interactive(step config.Step, finalState string) (Outcome, error) {
	fmt.Fprintf(p.Out, "Session %q ended (state: %s). [a]dvance / [r]etry / [x]abort: ", step.Name, finalState)
	return p.readChoice()
}

func (p *StdinPrompter) readChoice() (Outcome, error) {
	r := bufio.NewReader(p.In)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return OutcomeAbort, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "a", "advance", "":
		return OutcomeAdvance, nil
	case "r", "retry":
		return OutcomeRetry, nil
	case "x", "abort":
		return OutcomeAbort, nil
	default:
		return OutcomeAbort, nil
	}
}
