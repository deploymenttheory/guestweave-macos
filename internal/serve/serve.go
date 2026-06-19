// Package serve bridges the "weave serve" subcommand to the two server
// implementations: the HTTP REST API (internal/httpapi) and the MCP stdio
// server (internal/mcp). It exists to keep those two packages independent of
// one another while giving the command a single entry point.
//go:build darwin

package serve

import (
	"context"

	"github.com/deploymenttheory/weave/internal/httpapi"
	"github.com/deploymenttheory/weave/internal/mcp"
)

// ServeCommand ports the serve command.
type ServeCommand struct {
	Port uint16
	MCP  bool
}

func (c *ServeCommand) Run(ctx context.Context) error {
	if c.MCP {
		return mcp.RunMCPServer(ctx)
	}
	return httpapi.NewAPIServer(c.Port).Run(ctx)
}
