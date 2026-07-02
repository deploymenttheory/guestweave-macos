//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newCloneCommand() *cobra.Command {
	c := &weavecommand.CloneCommand{}

	cmd := &cobra.Command{
		Use:   "clone <source-name> <new-name>",
		Short: "Clone a local or remote VM",
		Long: `Clone a local VM, or pull a remote image and clone it in one step.

Identity is copied verbatim by default: the clone keeps the source's MAC
address and machine serial. Use --regenerate-random-mac and
--regenerate-random-serial when the clone will run alongside its source.`,
		Example: `  weave clone sequoia sequoia-work
  weave clone ghcr.io/org/images/ci:latest ci-runner
  weave clone sequoia sequoia-2 --regenerate-random-mac --regenerate-random-serial`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c.SourceName, c.NewName = args[0], args[1]
			if err := c.Validate(); err != nil {
				return err
			}
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&c.Insecure, "insecure", false, "connect to the registry over plain HTTP")
	flags.StringVar(&c.Registry, "registry", "", "registry profile to use for a remote source")
	flags.UintVar(&c.Concurrency, "concurrency", 4, "parallel layer downloads for a remote source")
	flags.BoolVar(&c.Deduplicate, "deduplicate", false, "clone the pulled image with APFS cloning to save space")
	flags.BoolVar(&c.RegenerateRandomMAC, "regenerate-random-mac", false, "give the clone a fresh random MAC address")
	flags.BoolVar(&c.RegenerateRandomSerial, "regenerate-random-serial", false, "give the clone a fresh machine identifier")
	flags.UintVar(&c.PruneLimit, "prune-limit", 100, "cache prune limit in GB when pulling a remote source")
	return cmd
}
