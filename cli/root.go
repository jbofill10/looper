// Package cli wires looper's command-line interface.
package cli

import (
	"fmt"

	"github.com/jbofill10/looper/client"
	"github.com/jbofill10/looper/daemon"
	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	var socket string
	root := &cobra.Command{
		Use:     "looper",
		Short:   "looper runs loop-based workflows",
		Version: daemon.Version,
		// A bare `looper` invocation (no subcommand) launches the TUI, the
		// same as `looper tui`. Cobra only invokes a parent's RunE when no
		// subcommand matched, so existing subcommands are unaffected.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTui(cmd, socket)
		},
	}
	root.AddCommand(newRunCmd())
	root.AddCommand(newResumeCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newHookCmd())
	root.AddCommand(newDaemonCmd())
	root.AddCommand(newPingCmd())
	root.AddCommand(newShutdownCmd())
	root.AddCommand(newStartCmd())
	root.AddCommand(newLsCmd())
	root.AddCommand(newStopCmd())
	root.AddCommand(newAttachCmd())
	root.AddCommand(newTuiCmd())
	root.AddCommand(newNewCmd())
	root.AddCommand(newEditCmd())
	root.Flags().StringVar(&socket, "socket", client.SocketPath(), "path to looperd's Unix socket")
	return root
}

// Execute runs the looper CLI.
func Execute() error {
	return newRootCmd().Execute()
}

// newVersionCmd builds the `looper version` subcommand, which prints
// looperd's version string (the CLI and daemon are versioned together).
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the looper version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "looper %s\n", daemon.Version)
			return nil
		},
	}
}
