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
	var concurrency int
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
				LoopName:    loopName,
				LoopFile:    file,
				BaseDir:     filepath.Join(wd, ".looper"),
				Workdir:     wd,
				Concurrency: int32(concurrency),
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
	cmd.Flags().IntVar(&concurrency, "concurrency", 0, "number of workers to run (0 = the loop's configured concurrency)")
	return cmd
}

// workerPrefix renders "<worker-id> · " for a worker-tagged update, or ""
// when u carries no worker id (single-worker run).
func workerPrefix(u *rpc.StateUpdate) string {
	if u.WorkerId == "" {
		return ""
	}
	return u.WorkerId + " · "
}

// printUpdate renders one state update as a concise, single-line summary,
// prefixed with the worker id when the update is worker-tagged.
func printUpdate(out io.Writer, u *rpc.StateUpdate) {
	prefix := workerPrefix(u)
	switch u.Kind {
	case "step_start":
		fmt.Fprintf(out, "%siter %d · %s · running\n", prefix, u.Iteration, u.Step)
	case "outcome":
		fmt.Fprintf(out, "%siter %d · %s · %s\n", prefix, u.Iteration, u.Step, u.State)
	case "decision_request":
		fmt.Fprintf(out, "%siter %d · %s · awaiting decision\n", prefix, u.Iteration, u.Step)
	case "run_done":
		fmt.Fprintf(out, "run %s\n", u.State)
	}
}

// promptDecision reads a single-letter choice from in ((a)dvance/(r)etry/
// (x)abort) and returns the corresponding decision outcome string. The
// prompt names which worker the decision is for when u is worker-tagged.
func promptDecision(out io.Writer, in *bufio.Reader, u *rpc.StateUpdate) string {
	if u.WorkerId != "" {
		fmt.Fprintf(out, "decision needed for %s, step %q. [a]dvance / [r]etry / [x]abort: ", u.WorkerId, u.Step)
	} else {
		fmt.Fprintf(out, "decision needed for step %q. [a]dvance / [r]etry / [x]abort: ", u.Step)
	}
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

			anyWorkers := false
			for _, r := range resp.Runs {
				if len(r.Workers) > 0 {
					anyWorkers = true
					break
				}
			}
			if anyWorkers {
				fmt.Fprintln(out)
				fmt.Fprintf(out, "%-16s %-10s %-16s %-16s %s\n", "RUN", "WORKER", "TASK", "STEP", "STATE")
				for _, r := range resp.Runs {
					for _, w := range r.Workers {
						fmt.Fprintf(out, "%-16s %-10s %-16s %-16s %s\n",
							r.RunId, w.WorkerId, w.Task, w.CurrentStep, w.State)
					}
				}
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
