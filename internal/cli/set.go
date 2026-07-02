//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newSetCommand() *cobra.Command {
	c := &weavecommand.SetCommand{}
	var (
		cpu            uint16
		memory         uint64
		display        string
		displayRefit   bool
		noDisplayRefit bool
		diskSize       uint16
	)

	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Modify a VM's configuration",
		Long: `Modify a stopped VM's configuration: CPU count, memory, display
geometry, identity (MAC address, machine serial), disk size and the
per-VM clipboard policy.`,
		Example: `  weave set sequoia --cpu 8 --memory 8192
  weave set sequoia --display 1920x1080
  weave set sequoia --random-mac
  weave set sequoia --clipboard-enabled true --clipboard-direction both`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeLocalMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Zero means "leave unchanged" for the numeric knobs, matching
			// the previous dispatcher.
			if cpu != 0 {
				c.CPU = &cpu
			}
			if memory != 0 {
				c.Memory = &memory
			}
			if display != "" {
				displayConfig := weavecommand.ParseVMDisplayConfig(display)
				c.Display = &displayConfig
			}
			if displayRefit {
				value := true
				c.DisplayRefit = &value
			} else if noDisplayRefit {
				value := false
				c.DisplayRefit = &value
			}
			if diskSize != 0 {
				c.DiskSize = &diskSize
			}
			c.Name = args[0]
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.Uint16Var(&cpu, "cpu", 0, "number of virtual CPUs")
	flags.Uint64Var(&memory, "memory", 0, "memory size in MB")
	flags.StringVar(&display, "display", "", "display geometry, e.g. 1920x1080 or 1920x1080:144")
	flags.BoolVar(&displayRefit, "display-refit", false, "resize the guest display to fit the window")
	flags.BoolVar(&noDisplayRefit, "no-display-refit", false, "keep the guest display at a fixed size")
	flags.BoolVar(&c.RandomMAC, "random-mac", false, "assign a fresh random MAC address")
	flags.BoolVar(&c.RandomSerial, "random-serial", false, "assign a fresh machine identifier")
	flags.StringVar(&c.Disk, "disk", "", "replace the root disk with the given image path")
	flags.Uint16Var(&diskSize, "disk-size", 0, "grow the root disk to the given size in GB")
	flags.StringVar(&c.Clipboard.Enabled, "clipboard-enabled", "", "enable or disable the clipboard policy (true|false)")
	flags.StringVar(&c.Clipboard.Direction, "clipboard-direction", "", "clipboard direction (both|host-to-guest|guest-to-host|off)")
	flags.StringVar(&c.Clipboard.Formats, "clipboard-formats", "", "allowed clipboard formats (comma-separated)")
	flags.StringVar(&c.Clipboard.Files, "clipboard-files", "", "allow file transfers over the clipboard (true|false)")
	flags.StringVar(&c.Clipboard.AllowedTypes, "clipboard-allowed-types", "", "allowed file types for clipboard transfers (comma-separated)")
	flags.StringVar(&c.Clipboard.Audit, "clipboard-audit", "", "audit clipboard traffic to the audit log (true|false)")
	flags.IntVar(&c.Clipboard.SessionMbps, "clipboard-session-mbps", 0, "clipboard session bandwidth cap in Mbps")
	flags.IntVar(&c.Clipboard.BandwidthPct, "clipboard-bandwidth-pct", 0, "clipboard bandwidth cap as a percent of link speed")
	flags.Int64Var(&c.Clipboard.MaxBytes, "clipboard-max-bytes", 0, "maximum clipboard payload size in bytes")
	return cmd
}
