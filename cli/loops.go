package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jbofill10/looper/client"
	"github.com/jbofill10/looper/rpc"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// streamRPCTimeout bounds the StartLoop/StopLoop/ListRuns/RespondDecision
// RPCs issued by the CLI (StreamState itself is long-lived and unbounded).
const streamRPCTimeout = 5 * time.Second

// newStartCmd builds the `looper start` subcommand, which starts a loop in
// the daemon (auto-spawning it if necessary) and streams its progress until
// the run finishes.
func newStartCmd() *cobra.Command {
	var file, socket string
	cmd := &cobra.Command{
		Use:   "start [loop-name]",
		Short: "Start a loop in the daemon and stream its progress",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var loopName string
			if len(args) == 1 {
				loopName = args[0]
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
			defer conn.Close()

			wd, err := os.Getwd()
			if err != nil {
				return err
			}

			startCtx, startCancel := context.WithTimeout(cmd.Context(), streamRPCTimeout)
			startResp, err := c.StartLoop(startCtx, &rpc.StartLoopRequest{
				LoopName: loopName,
				LoopFile: file,
				BaseDir:  filepath.Join(wd, ".looper"),
				Workdir:  wd,
			})
			startCancel()
			if err != nil {
				return fmt.Errorf("starting loop: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "run %s started\n", startResp.RunId)

			streamCtx, streamCancel := context.WithCancel(cmd.Context())
			defer streamCancel()
			stream, err := c.StreamState(streamCtx, &rpc.StreamStateRequest{RunId: startResp.RunId})
			if err != nil {
				return fmt.Errorf("streaming state: %w", err)
			}

			in := bufio.NewReader(cmd.InOrStdin())
			for {
				u, err := stream.Recv()
				if err != nil {
					return fmt.Errorf("stream ended: %w", err)
				}
				printUpdate(out, u)

				switch u.Kind {
				case "decision_request":
					outcome := promptDecision(out, in, u)
					respCtx, respCancel := context.WithTimeout(cmd.Context(), streamRPCTimeout)
					_, err := c.RespondDecision(respCtx, &rpc.RespondDecisionRequest{
						RunId: startResp.RunId, RequestId: u.RequestId, Outcome: outcome,
					})
					respCancel()
					if err != nil {
						return fmt.Errorf("responding to decision: %w", err)
					}
				case "run_done":
					if u.State == "error" || u.State == "stopped" {
						return fmt.Errorf("run %s finished with status %q: %s", startResp.RunId, u.State, u.Message)
					}
					return nil
				}
			}
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to a loop YAML file (overrides loop-name)")
	cmd.Flags().StringVar(&socket, "socket", client.SocketPath(), "path to looperd's Unix socket")
	return cmd
}

// printUpdate renders one state update as a concise, single-line summary.
func printUpdate(out io.Writer, u *rpc.StateUpdate) {
	switch u.Kind {
	case "step_start":
		fmt.Fprintf(out, "iter %d · %s · running\n", u.Iteration, u.Step)
	case "outcome":
		fmt.Fprintf(out, "iter %d · %s · %s\n", u.Iteration, u.Step, u.State)
	case "decision_request":
		fmt.Fprintf(out, "iter %d · %s · awaiting decision\n", u.Iteration, u.Step)
	case "run_done":
		fmt.Fprintf(out, "run %s\n", u.State)
	}
}

// promptDecision reads a single-letter choice from in ((a)dvance/(r)etry/
// (x)abort) and returns the corresponding decision outcome string.
func promptDecision(out io.Writer, in *bufio.Reader, u *rpc.StateUpdate) string {
	fmt.Fprintf(out, "decision needed for step %q. [a]dvance / [r]etry / [x]abort: ", u.Step)
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "a", "advance", "":
		return "advance"
	case "r", "retry":
		return "retry"
	default:
		return "abort"
	}
}

// newLsCmd builds the `looper ls` subcommand, which lists runs known to the
// daemon. Unlike start, it never spawns the daemon: if it isn't running, it
// prints a friendly message and exits 0.
func newLsCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List runs known to the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, conn, err := client.Dial(socket)
			if err != nil {
				return fmt.Errorf("dialing daemon: %w", err)
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), streamRPCTimeout)
			defer cancel()
			resp, err := c.ListRuns(ctx, &rpc.ListRunsRequest{})
			if err != nil {
				if isUnavailable(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "no daemon running")
					return nil
				}
				return fmt.Errorf("listing runs: %w", err)
			}

			out := cmd.OutOrStdout()
			if len(resp.Runs) == 0 {
				fmt.Fprintln(out, "no runs")
				return nil
			}
			fmt.Fprintf(out, "%-16s %-16s %-10s %-6s %-16s %s\n", "RUN ID", "LOOP", "STATUS", "ITER", "STEP", "STATE")
			for _, r := range resp.Runs {
				fmt.Fprintf(out, "%-16s %-16s %-10s %-6d %-16s %s\n",
					r.RunId, r.LoopName, r.Status, r.Iteration, r.CurrentStep, r.State)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&socket, "socket", client.SocketPath(), "path to looperd's Unix socket")
	return cmd
}

// newStopCmd builds the `looper stop` subcommand, which asks the daemon to
// stop a running loop. It never spawns the daemon.
func newStopCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "stop <run-id>",
		Short: "Stop a running loop",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, conn, err := client.Dial(socket)
			if err != nil {
				return fmt.Errorf("dialing daemon: %w", err)
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), streamRPCTimeout)
			defer cancel()
			if _, err := c.StopLoop(ctx, &rpc.StopLoopRequest{RunId: args[0]}); err != nil {
				if isUnavailable(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "no daemon running")
					return nil
				}
				return fmt.Errorf("stopping run %s: %w", args[0], err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "run %s stopped\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&socket, "socket", client.SocketPath(), "path to looperd's Unix socket")
	return cmd
}

// isUnavailable reports whether err indicates the daemon is unreachable
// (as opposed to a genuine RPC-level error from a running daemon).
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		return st.Code() == codes.Unavailable
	}
	return errors.Is(err, context.DeadlineExceeded)
}
