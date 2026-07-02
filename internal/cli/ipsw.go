//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newIPSWCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ipsw",
		Short: "Print the download URL of the latest supported macOS IPSW",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.IPSWCommand{}
			return runBackground(cmd, c.Run)
		},
	}
}
