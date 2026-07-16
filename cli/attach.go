package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jbofill10/looper/client"
	looperpty "github.com/jbofill10/looper/pty"
	"github.com/jbofill10/looper/rpc"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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

// runAttach dials the daemon, opens the Attach bidi stream for runID, and
// bridges it to the local terminal until the human detaches (Ctrl-b d) or
// the session/stream ends.
func runAttach(cmd *cobra.Command, runID, socket string) error {
	c, conn, err := client.Dial(socket)
	if err != nil {
		return fmt.Errorf("dialing daemon: %w", err)
	}
	defer conn.Close()

	stream, err := c.Attach(cmd.Context())
	if err != nil {
		return fmt.Errorf("opening attach stream: %w", err)
	}
	if err := stream.Send(&rpc.AttachInput{Msg: &rpc.AttachInput_Start{
		Start: &rpc.AttachStart{RunId: runID},
	}}); err != nil {
		return fmt.Errorf("sending attach start for run %s: %w", runID, err)
	}

	in, out := os.Stdin, os.Stdout
	fmt.Fprintln(os.Stderr, "-- attached; Ctrl-b d to detach --")

	isTerm := term.IsTerminal(int(in.Fd()))
	if isTerm {
		oldState, err := term.MakeRaw(int(in.Fd()))
		if err == nil {
			defer term.Restore(int(in.Fd()), oldState)
		}
		sendResize(stream, out)

		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				sendResize(stream, out)
			}
		}()
	}

	go forwardStdin(in, stream)

	for {
		outMsg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("attach stream: %w", err)
		}
		if _, err := out.Write(outMsg.GetData()); err != nil {
			return fmt.Errorf("writing session output: %w", err)
		}
	}
}

// sendResize sends the terminal size of out as a Resize message,
// best-effort (a failure to query the size just skips the resize).
func sendResize(stream rpc.Looper_AttachClient, out *os.File) {
	cols, rows, err := term.GetSize(int(out.Fd()))
	if err != nil {
		return
	}
	_ = stream.Send(&rpc.AttachInput{Msg: &rpc.AttachInput_Resize{
		Resize: &rpc.Resize{Rows: uint32(rows), Cols: uint32(cols)},
	}})
}

// forwardStdin reads in, scanning for the Ctrl-b d detach escape, and sends
// passthrough bytes as session input. It closes the stream's send direction
// (signalling detach or "no more input" to the daemon, which leaves the
// session itself running) once the human detaches or in reaches EOF/error.
func forwardStdin(in *os.File, stream rpc.Looper_AttachClient) {
	var scanner looperpty.DetachScanner
	buf := make([]byte, 4096)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			passthrough, detached := scanner.Scan(buf[:n])
			if len(passthrough) > 0 {
				if sendErr := stream.Send(&rpc.AttachInput{Msg: &rpc.AttachInput_Data{
					Data: passthrough,
				}}); sendErr != nil {
					return
				}
			}
			if detached {
				_ = stream.CloseSend()
				return
			}
		}
		if err != nil {
			_ = stream.CloseSend()
			return
		}
	}
}
