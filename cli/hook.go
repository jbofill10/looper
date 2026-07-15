package cli

import (
	"fmt"
	"io"
	"net"

	"github.com/spf13/cobra"
)

// forwardHook reads all of in and writes it to the Unix socket at
// socketPath, forwarding a Claude Code hook payload to a running looper
// session listener.
func forwardHook(in io.Reader, socketPath string) error {
	data, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read hook payload: %w", err)
	}
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("dial hook socket %s: %w", socketPath, err)
	}
	defer conn.Close()
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write hook payload: %w", err)
	}
	return nil
}

// newHookCmd builds the hidden `looper hook` subcommand, used internally as
// the command Claude Code hooks invoke to forward hook events to looper.
func newHookCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Forward a Claude Code hook payload to a looper session socket",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return forwardHook(cmd.InOrStdin(), socket)
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "path to the session's Unix socket")
	cmd.MarkFlagRequired("socket")
	return cmd
}
