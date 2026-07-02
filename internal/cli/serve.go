//go:build darwin

package cli

import (
	"github.com/deploymenttheory/guestweave/internal/serve"
	"github.com/spf13/cobra"
)

func newServeCommand() *cobra.Command {
	c := &serve.ServeCommand{}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the weave REST API server",
		Long: `Run the weave REST API server, optionally exposing the MCP
endpoint for AI-agent integrations.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.Uint16Var(&c.Port, "port", 7777, "TCP port to listen on")
	flags.BoolVar(&c.MCP, "mcp", false, "expose the MCP endpoint")
	return cmd
}
