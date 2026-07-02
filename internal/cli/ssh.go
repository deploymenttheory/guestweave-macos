//go:build darwin

package cli

import (
	"fmt"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/deploymenttheory/guestweave/internal/macaddress"
	"github.com/spf13/cobra"
)

func newSSHCommand() *cobra.Command {
	c := &weavecommand.SSHCommand{}
	var resolver string

	cmd := &cobra.Command{
		Use:   "ssh <name> [command...]",
		Short: "Open an SSH session to a running VM",
		Long: `Open an SSH session to a running VM, resolving its IP automatically.
Without a command, an interactive shell is opened.

Flags must precede the VM name; everything after the name is run remotely.`,
		Example: `  weave ssh sequoia
  weave ssh sequoia uname -a
  weave ssh -u admin -p secret sequoia`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeRunningMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			strategy, ok := macaddress.ParseIPResolutionStrategy(resolver)
			if !ok {
				return fmt.Errorf("unsupported resolver: %q", resolver)
			}
			c.Resolver = strategy
			c.Name = args[0]
			c.Command = args[1:]
			return runBackground(cmd, c.Run)
		},
	}

	// Stop flag parsing at the VM name so the remote command is captured
	// verbatim, mirroring exec's passthrough handling.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVarP(&c.User, "user", "u", "weave", "guest username")
	cmd.Flags().StringVarP(&c.Password, "password", "p", "weave", "guest password")
	cmd.Flags().Uint64VarP(&c.Timeout, "timeout", "t", 60, "connection timeout in seconds (0 = no timeout)")
	cmd.Flags().Uint16Var(&c.Wait, "wait", 0, "seconds to keep retrying IP resolution")
	cmd.Flags().StringVar(&resolver, "resolver", "dhcp", "IP resolution strategy (dhcp|arp|agent)")
	return cmd
}
