package client

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	looperpty "github.com/jbofill10/looper/pty"
	"github.com/jbofill10/looper/rpc"
	"golang.org/x/term"
)

// AttachStream opens the Attach bidi RPC for runID on cl and bridges it to
// in/out until the human detaches (Ctrl-b d), the session ends, or ctx is
// cancelled. When in is a terminal, it is switched to raw mode for the
// duration (and restored on return) and SIGWINCH triggers a resize message.
// It is shared by the `looper attach` CLI command and the TUI's attach
// action, so both use identical framing/detach/resize behavior.
func AttachStream(ctx context.Context, cl rpc.LooperClient, runID string, in, out *os.File) error {
	stream, err := cl.Attach(ctx)
	if err != nil {
		return fmt.Errorf("opening attach stream: %w", err)
	}
	if err := stream.Send(&rpc.AttachInput{Msg: &rpc.AttachInput_Start{
		Start: &rpc.AttachStart{RunId: runID},
	}}); err != nil {
		return fmt.Errorf("sending attach start for run %s: %w", runID, err)
	}

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
