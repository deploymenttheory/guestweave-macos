//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newSuspendCommand() *cobra.Command {
	c := &weavecommand.SuspendCommand{}

	cmd := &cobra.Command{
		Use:   "suspend <name>",
		Short: "Suspend a running VM to disk",
		Long: `Suspend a running VM to disk so a later "weave run" resumes it
instantly. Requires macOS 14 or newer and a VM started with --suspendable.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRunningMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c.Name = args[0]
			return runBackground(cmd, c.Run)
		},
	}
	return cmd
}
