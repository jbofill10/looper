package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jbofill10/looper/builder"
	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/stepauthor"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	tea "github.com/charmbracelet/bubbletea"
)

// runBuilder loads the global config, constructs the file-backed builder
// for loopPath (creating it if it doesn't exist), and runs it until the
// user quits.
func runBuilder(loopPath, wd string) (builder.Model, error) {
	global, err := config.LoadGlobal(globalPath())
	if err != nil {
		return builder.Model{}, fmt.Errorf("loading global config: %w", err)
	}

	var p *tea.Program
	m, err := builder.New(wd, loopPath, builder.Options{AuthorFn: authorFn(&p, global, wd)})
	if err != nil {
		return builder.Model{}, err
	}
	p = tea.NewProgram(m)

	final, err := p.Run()
	if err != nil {
		return builder.Model{}, fmt.Errorf("running builder: %w", err)
	}
	fm, ok := final.(builder.Model)
	if !ok {
		return builder.Model{}, fmt.Errorf("builder produced an unexpected model type")
	}
	return fm, nil
}

// authorFn returns the builder.Options.AuthorFn implementation for the
// standalone CLI builder: it releases the Bubble Tea program's hold on
// the terminal, runs a create/edit-step session via the stepauthor
// package against the "claude" harness, and restores the program's
// terminal control on return. pp captures the *tea.Program variable that
// runBuilder assigns after constructing it (authorFn is built before the
// Program exists, so it captures the variable, not its value).
func authorFn(pp **tea.Program, global *config.Global, wd string) func(builder.AuthorRequest) tea.Cmd {
	return func(req builder.AuthorRequest) tea.Cmd {
		return func() tea.Msg {
			p := *pp
			if p != nil {
				if err := p.ReleaseTerminal(); err != nil {
					return builder.SessionDoneMsg{Err: err}
				}
				defer p.RestoreTerminal()
			}

			h, err := global.ResolveHarness("claude")
			if err != nil {
				return builder.SessionDoneMsg{Err: err}
			}

			if req.StepName == "" {
				err = stepauthor.CreateStep(req.ProjectDir, h, req.LoopPath)
			} else {
				err = stepauthor.EditStep(req.ProjectDir, h, req.LoopPath, req.StepName, req.ValidationErr)
			}
			return builder.SessionDoneMsg{Err: err}
		}
	}
}

// notATerminal reports whether stdin or stdout is not a terminal, printing
// a hint to out when true. Launching an interactive Bubble Tea program on a
// non-terminal would hang forever (no terminal to read keys from or render
// into), so callers should skip straight to returning nil.
func notATerminal(cmd *cobra.Command) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(cmd.OutOrStdout(), "looper new/edit: stdin/stdout is not a terminal; run it from an interactive terminal.")
		return true
	}
	return false
}

// newNewCmd builds the `looper new <name>` subcommand, which opens the
// file-backed builder for <cwd>/.looper/loops/<name>.yaml, creating it
// first if it doesn't exist.
func newNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a new loop with the interactive builder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if notATerminal(cmd) {
				return nil
			}
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}
			loopPath := filepath.Join(wd, ".looper", "loops", args[0]+".yaml")

			fm, err := runBuilder(loopPath, wd)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", fm.Path())
			return nil
		},
	}
	return cmd
}

// newEditCmd builds the `looper edit <name>` subcommand, which opens the
// file-backed builder for an existing loop.
func newEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit an existing loop with the interactive builder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}
			loopPath := filepath.Join(wd, ".looper", "loops", args[0]+".yaml")
			if _, err := config.LoadLoop(loopPath); err != nil {
				return fmt.Errorf("loading loop %q: %w", args[0], err)
			}

			if notATerminal(cmd) {
				return nil
			}

			fm, err := runBuilder(loopPath, wd)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", fm.Path())
			return nil
		},
	}
	return cmd
}
