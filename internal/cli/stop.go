//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newStopCommand() *cobra.Command {
	c := &weavecommand.StopCommand{}

	cmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a running VM",
		Long: `Stop a running VM by asking its run process to shut down gracefully,
force-killing it after the timeout. Stopping a suspended VM discards its
saved state.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRunningMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c.Name = args[0]
			return runBackground(cmd, c.Run)
		},
	}

	cmd.Flags().Uint64VarP(&c.Timeout, "timeout", "t", 30, "seconds to wait for graceful termination")
	return cmd
}
