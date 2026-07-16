package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jbofill10/looper/client"
	"github.com/jbofill10/looper/daemon"
	"github.com/jbofill10/looper/rpc"
	"github.com/spf13/cobra"
)

// rpcTimeout bounds the ping/shutdown RPCs issued by the CLI.
const rpcTimeout = 3 * time.Second

// newDaemonCmd builds the `looper daemon` subcommand, which runs looperd in
// the foreground on the given socket. This is what auto-spawn invokes, and
// what you run directly to debug the daemon.
func newDaemonCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:    "daemon",
		Short:  "Run the looper daemon in the foreground",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			srv := daemon.New()

			// Install SIGINT/SIGTERM handling so Ctrl-C (or a controlled
			// `kill`) stops the daemon gracefully: Stop() drains in-flight
			// RPCs and Serve's own deferred os.Remove cleans up the socket
			// file, instead of the process dying uncleanly and leaving a
			// stale socket behind.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sigCh)
			go func() {
				if _, ok := <-sigCh; ok {
					srv.Stop()
				}
			}()

			return srv.Serve(socket)
		},
	}
	cmd.Flags().StringVar(&socket, "socket", client.SocketPath(), "path to looperd's Unix socket")
	return cmd
}

// newPingCmd builds the `looper ping` subcommand, which ensures looperd is
// running (auto-spawning it if necessary) and prints its version.
func newPingCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "ping",
		Short: "Check that the looper daemon is running",
		RunE: func(cmd *cobra.Command, args []string) error {
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

			ctx, cancel := context.WithTimeout(cmd.Context(), rpcTimeout)
			defer cancel()
			resp, err := c.Ping(ctx, &rpc.PingRequest{})
			if err != nil {
				return fmt.Errorf("ping: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "looperd %s\n", resp.Version)
			return nil
		},
	}
	cmd.Flags().StringVar(&socket, "socket", client.SocketPath(), "path to looperd's Unix socket")
	return cmd
}

// newShutdownCmd builds the `looper shutdown` subcommand, which asks a
// running looperd to stop gracefully. Unlike ping, it never auto-spawns the
// daemon: if it isn't running, it prints a friendly message and exits 0.
func newShutdownCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "shutdown",
		Short: "Stop the looper daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, conn, err := client.Dial(socket)
			if err != nil {
				return fmt.Errorf("dialing daemon: %w", err)
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), rpcTimeout)
			defer cancel()
			if _, err := c.Shutdown(ctx, &rpc.ShutdownRequest{}); err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "looper daemon is not running")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "looper daemon stopped")
			return nil
		},
	}
	cmd.Flags().StringVar(&socket, "socket", client.SocketPath(), "path to looperd's Unix socket")
	return cmd
}
