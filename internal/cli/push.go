//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newPushCommand() *cobra.Command {
	c := &weavecommand.PushCommand{}

	cmd := &cobra.Command{
		Use:   "push <local-name> <remote-name>...",
		Short: "Push a local VM to an OCI registry",
		Example: `  weave push sequoia ghcr.io/org/images/sequoia:latest
  weave push sequoia sequoia:v1 sequoia:latest --registry work`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeLocalMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c.LocalName = args[0]
			c.RemoteNames = args[1:]
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&c.Insecure, "insecure", false, "connect to the registry over plain HTTP")
	flags.StringVar(&c.Registry, "registry", "", "registry profile to resolve bare image names against")
	flags.UintVar(&c.Concurrency, "concurrency", 4, "parallel layer uploads")
	flags.IntVar(&c.ChunkSize, "chunk-size", 0, "upload chunk size in bytes (0 = single request)")
	flags.StringArrayVar(&c.Labels, "label", nil, "OCI annotation to attach, key=value (repeatable)")
	flags.BoolVar(&c.PopulateCache, "populate-cache", false, "also store the pushed image in the local OCI cache")
	return cmd
}
