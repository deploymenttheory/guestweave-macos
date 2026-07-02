//go:build darwin

package cli

import (
	"fmt"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/deploymenttheory/guestweave/internal/macaddress"
	"github.com/spf13/cobra"
)

func newIPCommand() *cobra.Command {
	c := &weavecommand.IPCommand{}
	var resolver string

	cmd := &cobra.Command{
		Use:   "ip <name>",
		Short: "Print a running VM's IP address",
		Example: `  weave ip sequoia
  weave ip sequoia --wait 120
  weave ip sequoia --resolver arp`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRunningMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			strategy, ok := macaddress.ParseIPResolutionStrategy(resolver)
			if !ok {
				return fmt.Errorf("unsupported resolver: %q", resolver)
			}
			c.Resolver = strategy
			c.Name = args[0]
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.Uint16Var(&c.Wait, "wait", 0, "seconds to keep retrying until an IP is found")
	flags.StringVar(&resolver, "resolver", "dhcp", "IP resolution strategy (dhcp|arp|agent)")
	return cmd
}
