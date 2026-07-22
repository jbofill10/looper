package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runner"
	"github.com/spf13/cobra"
)

// globalPath returns the path to looper's global config file:
// $XDG_CONFIG_HOME/looper/config.yaml, or ~/.config/looper/config.yaml.
func globalPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "looper", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "looper", "config.yaml")
	}
	return filepath.Join(home, ".config", "looper", "config.yaml")
}

// RunOptions configures a single `looper run` invocation.
type RunOptions struct {
	LoopName string    // loads BaseDir/loops/<LoopName>.yaml when File is empty
	File     string    // explicit loop file path (overrides LoopName)
	BaseDir  string    // the .looper directory
	In       io.Reader // prompter input (defaults to os.Stdin)
	Out      io.Writer // prompter/output (defaults to os.Stdout)

	// ResumeDir, if set, is passed through to runner.Worker.ResumeDir: the
	// first iteration resumes from that existing iteration directory
	// instead of starting fresh. Used by `looper resume`.
	ResumeDir string
}

// RunLoop loads a loop and runs it single-worker, in-process.
func RunLoop(opts RunOptions) error {
	path := opts.File
	if path == "" {
		if opts.LoopName == "" {
			return fmt.Errorf("either a loop name or --file is required")
		}
		path = filepath.Join(opts.BaseDir, "loops", opts.LoopName+".yaml")
	}
	loop, err := config.LoadLoop(path)
	if err != nil {
		return err
	}
	global, err := config.LoadGlobal(globalPath())
	if err != nil {
		return err
	}

	in := opts.In
	if in == nil {
		in = os.Stdin
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	looperBin, err := os.Executable()
	if err != nil {
		looperBin = "looper"
	}

	w := &runner.Worker{
		Loop:      loop,
		BaseDir:   opts.BaseDir,
		Prompter:  &runner.StdinPrompter{In: in, Out: out},
		Global:    global,
		LooperBin: looperBin,
		ResumeDir: opts.ResumeDir,
	}
	fmt.Fprintf(out, "running loop %q\n", loop.Name)
	if err := w.Run(); err != nil {
		return err
	}
	fmt.Fprintf(out, "loop %q finished\n", loop.Name)
	return nil
}

// newRunCmd builds the `looper run` subcommand.
func newRunCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "run [loop-name]",
		Short: "Run a loop",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			opts := RunOptions{
				File:    file,
				BaseDir: filepath.Join(wd, ".looper"),
				In:      cmd.InOrStdin(),
				Out:     cmd.OutOrStdout(),
			}
			if len(args) == 1 {
				opts.LoopName = args[0]
			}
			return RunLoop(opts)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to a loop YAML file (overrides loop-name)")
	return cmd
}
