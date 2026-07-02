//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newExportCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "export <name> [path]",
		Short:             "Export a VM to an archive",
		Long:              `Export a VM to a transferable archive (defaults to "<name>.tvm").`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeLocalMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.ExportCommand{Name: args[0]}
			if len(args) == 2 {
				c.Path = args[1]
			}
			return runBackground(cmd, c.Run)
		},
	}
}
