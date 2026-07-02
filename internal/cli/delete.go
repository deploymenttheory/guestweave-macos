//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <name>...",
		Short: "Delete one or more VMs",
		Args:  cobra.MinimumNArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			// Multiple names are accepted, so complete at every position.
			return listMachines(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.DeleteCommand{Names: args}
			return runBackground(cmd, c.Run)
		},
	}
	return cmd
}
