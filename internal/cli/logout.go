//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <host>",
		Short: "Remove stored credentials for a registry host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.LogoutCommand{Host: args[0]}
			return runBackground(cmd, c.Run)
		},
	}
}
