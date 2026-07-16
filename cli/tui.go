package cli

import (
	"fmt"
	"os"

	"github.com/jbofill10/looper/client"
	"github.com/jbofill10/looper/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// newTuiCmd builds the `looper tui` subcommand, which launches the fleet &
// focus Bubble Tea client. It is also what a bare `looper` invocation (no
// subcommand) runs — see newRootCmd.
func newTuiCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Launch the interactive fleet & focus TUI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTui(cmd, socket)
		},
	}
	cmd.Flags().StringVar(&socket, "socket", client.SocketPath(), "path to looperd's Unix socket")
	return cmd
}

// runTui ensures the daemon is running, dials it, and runs the TUI. If
// stdin or stdout is not a terminal, launching the interactive program
// would hang (it has no terminal to read keys from or render into), so it
// prints a hint and returns immediately instead.
func runTui(cmd *cobra.Command, socket string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(cmd.OutOrStdout(), "looper tui: stdin/stdout is not a terminal; run it from an interactive terminal, or use `looper ls` / `looper start` instead.")
		return nil
	}

	self, err := os.Executable()
	if err != nil {
		self = "looper"
	}
	if err := client.EnsureDaemon(socket, self); err != nil {
		return fmt.Errorf("ensuring daemon is running: %w", err)
	}

	c, conn, err := client.Dial(socket)
	if err != nil {
		return fmt.Errorf("dialing daemon: %w", err)
	}

	return tui.Run(cmd.Context(), c, conn)
}
