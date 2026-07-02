//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newHvmmCommand() *cobra.Command {
	c := &weavecommand.HvmmCommand{}

	cmd := &cobra.Command{
		Use:   "hvmm [action] [firmware]",
		Short: "Experimental Hypervisor.framework EL2 VMM backend",
		Long: `Drive the experimental Hypervisor.framework EL2 VMM backend.
Actions: "test" (default) runs the harness self-test; "boot" boots an ARM64
UEFI firmware image.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				c.Action = args[0]
			}
			if len(args) > 1 {
				c.Firmware = args[1]
			}
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.IntVar(&c.MaxExits, "max-exits", 0, "bound the boot run by device-exit count (0 = unbounded)")
	flags.BoolVar(&c.Step, "step", false, "single-step trace the firmware's control flow")
	return cmd
}
