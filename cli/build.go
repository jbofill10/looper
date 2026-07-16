package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jbofill10/looper/builder"
	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/draft"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	tea "github.com/charmbracelet/bubbletea"
)

// buildAndSave assembles the loop from a completed builder.Model and saves
// it to <dir>/.looper/loops/<name>.yaml via config.SaveLoop, returning the
// path written. It errors if the builder has not reached its done stage.
func buildAndSave(m builder.Model, dir string) (string, error) {
	loop, ok := m.Loop()
	if !ok {
		return "", fmt.Errorf("builder has not finished")
	}
	path := filepath.Join(dir, ".looper", "loops", loop.Name+".yaml")
	if err := config.SaveLoop(loop, path); err != nil {
		return "", err
	}
	return path, nil
}

// runBuilder loads the global config, constructs the guided builder
// (pre-populated from existing if non-nil, offering its configured
// harnesses and a draft-session hook rooted at wd), and runs it to
// completion (or until the user quits without finishing).
func runBuilder(existing *config.Loop, wd string) (builder.Model, error) {
	global, err := config.LoadGlobal(globalPath())
	if err != nil {
		return builder.Model{}, fmt.Errorf("loading global config: %w", err)
	}

	var p *tea.Program
	m := builder.New(existing, global.HarnessNames(), builder.Options{DraftFn: draftFn(&p, global, wd)})
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

// draftFn returns the builder.Options.DraftFn implementation for the
// standalone CLI builder: it releases the Bubble Tea program's hold on the
// terminal, runs a draft session via the draft package using the global
// default harness, and restores the program's terminal control on return.
// pp captures the *tea.Program variable that runBuilder assigns after
// constructing it (draftFn is built before the Program exists, so it
// captures the variable, not its value) — mirroring tui/program.go's
// attachFn/draftFn.
func draftFn(pp **tea.Program, global *config.Global, wd string) func(builder.DraftRequest) tea.Cmd {
	return func(req builder.DraftRequest) tea.Cmd {
		return func() tea.Msg {
			p := *pp
			if p != nil {
				if err := p.ReleaseTerminal(); err != nil {
					return builder.DraftedMsg{Err: err}
				}
				defer p.RestoreTerminal()
			}

			h, err := global.ResolveHarness("")
			if err != nil {
				return builder.DraftedMsg{Err: err}
			}
			content, err := draft.Run(wd, h, draft.Request{
				LoopName:   req.LoopName,
				StepName:   req.StepName,
				PriorSteps: req.PriorSteps,
			})
			if err != nil {
				return builder.DraftedMsg{Err: err}
			}
			return builder.DraftedMsg{Content: content}
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

// newNewCmd builds the `looper new [name]` subcommand, which launches the
// guided loop builder and saves the resulting loop to
// <cwd>/.looper/loops/<name>.yaml.
func newNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new [name]",
		Short: "Create a new loop with the guided builder",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if notATerminal(cmd) {
				return nil
			}

			var existing *config.Loop
			if len(args) == 1 {
				existing = &config.Loop{Name: args[0]}
			}

			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}

			fm, err := runBuilder(existing, wd)
			if err != nil {
				return err
			}
			if !fm.Done() {
				fmt.Fprintln(cmd.OutOrStdout(), "looper new: cancelled")
				return nil
			}

			path, err := buildAndSave(fm, wd)
			if err != nil {
				return fmt.Errorf("saving loop: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
			return nil
		},
	}
	return cmd
}

// newEditCmd builds the `looper edit <name>` subcommand, which loads an
// existing loop, launches the guided builder pre-populated from it, and
// saves the result back to the same file.
func newEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit an existing loop with the guided builder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}
			path := filepath.Join(wd, ".looper", "loops", args[0]+".yaml")
			existing, err := config.LoadLoop(path)
			if err != nil {
				return fmt.Errorf("loading loop %q: %w", args[0], err)
			}

			if notATerminal(cmd) {
				return nil
			}

			fm, err := runBuilder(existing, wd)
			if err != nil {
				return err
			}
			if !fm.Done() {
				fmt.Fprintln(cmd.OutOrStdout(), "looper edit: cancelled")
				return nil
			}

			savedPath, err := buildAndSave(fm, wd)
			if err != nil {
				return fmt.Errorf("saving loop: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", savedPath)
			return nil
		},
	}
	return cmd
}
