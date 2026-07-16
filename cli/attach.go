package cli

import (
	"fmt"
	"os"

	"github.com/jbofill10/looper/client"
	"github.com/spf13/cobra"
)

// newAttachCmd builds the `looper attach` subcommand, which attaches the
// local terminal to a run's live interactive session over the daemon's
// Attach RPC: stdin is forwarded to the session (raw mode when stdin is a
// terminal), the session's output is written to stdout, and Ctrl-b d
// detaches without stopping the session (tmux-style).
func newAttachCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "attach <run-id>",
		Short: "Attach to a run's live interactive session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttach(cmd, args[0], socket)
		},
	}
	cmd.Flags().StringVar(&socket, "socket", client.SocketPath(), "path to looperd's Unix socket")
	return cmd
}

// runAttach dials the daemon and bridges the local terminal to runID's live
// interactive session via the shared client.AttachStream, until the human
// detaches (Ctrl-b d) or the session/stream ends.
func runAttach(cmd *cobra.Command, runID, socket string) error {
	c, conn, err := client.Dial(socket)
	if err != nil {
		return fmt.Errorf("dialing daemon: %w", err)
	}
	defer conn.Close()

	return client.AttachStream(cmd.Context(), c, runID, os.Stdin, os.Stdout)
}
