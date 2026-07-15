// Package cli wires looper's command-line interface.
package cli

import "github.com/spf13/cobra"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "looper",
		Short: "looper runs loop-based workflows",
	}
	root.AddCommand(newRunCmd())
	return root
}

// Execute runs the looper CLI.
func Execute() error {
	return newRootCmd().Execute()
}
