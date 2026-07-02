//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newImagesCommand() *cobra.Command {
	c := &weavecommand.ImagesCommand{}

	cmd := &cobra.Command{
		Use:   "images <host>/<repository> | <repository>",
		Short: "List images in an OCI repository",
		Example: `  weave images ghcr.io/org/images
  weave images images --registry work`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c.Repository = args[0]
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&c.Insecure, "insecure", false, "connect to the registry over plain HTTP")
	flags.StringVar(&c.Registry, "registry", "", "registry profile to resolve bare repository names against")
	flags.BoolVarP(&c.Quiet, "quiet", "q", false, "print image references only")
	return cmd
}
