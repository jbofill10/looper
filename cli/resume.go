package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jbofill10/looper/config"
	"github.com/jbofill10/looper/runctx"
	"github.com/spf13/cobra"
)

// findResumeDir scans <baseDir>/runs/<loopName>/ (including any worker
// subdirectories, e.g. runs/<loop>/w1/<iter>) for the most recent iteration
// directory that is eligible to resume: one with a context.json and a
// progress.json whose Done is false, or a context.json with no progress.json
// at all (an iteration interrupted before its first step completed).
// Iterations whose progress.json has Done:true are ignored. "Most recent" is
// judged by the newest of context.json's and progress.json's mtimes. It
// returns ("", false) if no eligible iteration directory is found.
func findResumeDir(baseDir, loopName string) (string, bool) {
	root := filepath.Join(baseDir, "runs", loopName)

	var bestDir string
	var bestTime time.Time
	found := false

	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		hasContext := false
		for _, e := range entries {
			if !e.IsDir() && e.Name() == "context.json" {
				hasContext = true
				break
			}
		}
		if hasContext {
			mtime, eligible := iterEligibility(dir)
			if eligible && (!found || mtime.After(bestTime)) {
				bestDir, bestTime, found = dir, mtime, true
			}
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				walk(filepath.Join(dir, e.Name()))
			}
		}
	}
	walk(root)

	return bestDir, found
}

// iterEligibility reports whether the iteration directory dir is eligible to
// resume (progress.json missing, or present with Done:false), and the mtime
// to rank it by (the newest of context.json's and progress.json's mtimes).
func iterEligibility(dir string) (time.Time, bool) {
	var mtime time.Time
	if info, err := os.Stat(filepath.Join(dir, "context.json")); err == nil {
		mtime = info.ModTime()
	}

	progPath := filepath.Join(dir, "progress.json")
	info, err := os.Stat(progPath)
	if err != nil {
		// No progress.json at all: treat as an incomplete iteration.
		return mtime, true
	}
	if info.ModTime().After(mtime) {
		mtime = info.ModTime()
	}

	rc, err := runctx.Load(dir)
	if err != nil {
		return mtime, false
	}
	p, err := rc.LoadProgress()
	if err != nil {
		return mtime, false
	}
	return mtime, !p.Done
}

// newResumeCmd builds the `looper resume` subcommand, which finds the most
// recent incomplete iteration of a loop and continues it in-process,
// reusing its persisted context and skipping steps already completed.
func newResumeCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "resume [loop-name]",
		Short: "Resume the most recently interrupted iteration of a loop",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			baseDir := filepath.Join(wd, ".looper")

			opts := RunOptions{
				File:    file,
				BaseDir: baseDir,
				Workdir: wd,
				In:      cmd.InOrStdin(),
				Out:     cmd.OutOrStdout(),
			}
			if len(args) == 1 {
				opts.LoopName = args[0]
			}

			loopPath := opts.File
			if loopPath == "" {
				if opts.LoopName == "" {
					return fmt.Errorf("either a loop name or --file is required")
				}
				loopPath = filepath.Join(baseDir, "loops", opts.LoopName+".yaml")
			}
			loop, err := config.LoadLoop(loopPath)
			if err != nil {
				return err
			}

			dir, ok := findResumeDir(baseDir, loop.Name)
			if !ok {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to resume")
				return nil
			}
			opts.LoopName = loop.Name
			opts.ResumeDir = dir
			return RunLoop(opts)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to a loop YAML file (overrides loop-name)")
	return cmd
}
