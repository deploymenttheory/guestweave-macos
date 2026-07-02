//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newExecCommand() *cobra.Command {
	c := &weavecommand.ExecCommand{}

	cmd := &cobra.Command{
		Use:   "exec [-it] <name> <command> [args...]",
		Short: "Execute a command in a running VM via the guest agent",
		Long: `Execute a command in a running VM over the control socket, without
SSH. The remote command's exit code becomes weave's exit code.

Flags must precede the VM name; everything after the name is passed to the
guest verbatim.`,
		Example: `  weave exec sequoia uname -a
  weave exec -it sequoia zsh`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeRunningMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c.Name = args[0]
			c.Command = args[1:]
			return runBackground(cmd, c.Run)
		},
	}

	// Stop flag parsing at the VM name so the remote command's own flags are
	// captured verbatim (ArgumentParser's .captureForPassthrough).
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().BoolVarP(&c.Interactive, "interactive", "i", false, "keep stdin open and forward it to the guest")
	cmd.Flags().BoolVarP(&c.TTY, "tty", "t", false, "allocate a pseudo-terminal in the guest")
	return cmd
}
