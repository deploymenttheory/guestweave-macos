//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newImportCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "import <path> <name>",
		Short: "Import a VM from an exported archive",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.ImportCommand{Path: args[0], Name: args[1]}
			if err := c.Validate(); err != nil {
				return err
			}
			return runBackground(cmd, c.Run)
		},
	}
}
