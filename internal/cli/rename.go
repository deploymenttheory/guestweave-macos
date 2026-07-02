//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newRenameCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "rename <name> <new-name>",
		Short:             "Rename a local VM",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeLocalMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.RenameCommand{Name: args[0], NewName: args[1]}
			if err := c.Validate(); err != nil {
				return err
			}
			return runBackground(cmd, c.Run)
		},
	}
	return cmd
}
