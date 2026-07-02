//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newLoginCommand() *cobra.Command {
	c := &weavecommand.LoginCommand{}

	cmd := &cobra.Command{
		Use:   "login <host>",
		Short: "Store credentials for an OCI registry host",
		Example: `  weave login ghcr.io --username me --password-stdin < token.txt
  weave login registry.local:5000 --insecure`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c.Host = args[0]
			if err := c.Validate(); err != nil {
				return err
			}
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&c.Username, "username", "", "registry username")
	flags.BoolVar(&c.PasswordStdin, "password-stdin", false, "read the password from standard input")
	flags.BoolVar(&c.Insecure, "insecure", false, "connect over plain HTTP")
	flags.BoolVar(&c.NoValidate, "no-validate", false, "store the credentials without validating them")
	return cmd
}
