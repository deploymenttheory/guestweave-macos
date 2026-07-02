//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newPullCommand() *cobra.Command {
	c := &weavecommand.PullCommand{}

	cmd := &cobra.Command{
		Use:   "pull <remote-name>",
		Short: "Pull a VM image from an OCI registry",
		Example: `  weave pull ghcr.io/org/images/sequoia:latest
  weave pull sequoia:latest --registry work`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c.RemoteName = args[0]
			if err := c.Validate(); err != nil {
				return err
			}
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&c.Insecure, "insecure", false, "connect to the registry over plain HTTP")
	flags.StringVar(&c.Registry, "registry", "", "registry profile to resolve bare image names against")
	flags.UintVar(&c.Concurrency, "concurrency", 4, "parallel layer downloads")
	flags.BoolVar(&c.Deduplicate, "deduplicate", false, "deduplicate the pulled disk against the cache with APFS cloning")
	return cmd
}
