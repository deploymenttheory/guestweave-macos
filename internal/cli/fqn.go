//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newFQNCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "fqn <name>",
		Short:             "Print the fully-qualified name of a cached image",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.FQNCommand{Name: args[0]}
			return runBackground(cmd, c.Run)
		},
	}
}
