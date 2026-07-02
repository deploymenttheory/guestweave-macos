//go:build darwin

package cli

import (
	"context"
	"fmt"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/deploymenttheory/guestweave/internal/diskimage"
	"github.com/spf13/cobra"
)

func newCreateCommand() *cobra.Command {
	c := &weavecommand.CreateCommand{}
	var diskFormat string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new virtual machine",
		Long: `Create a new virtual machine.

macOS guests are installed from an IPSW restore image (--from-ipsw, or
"latest" to download the newest supported build). Linux guests boot an empty
EFI VM ready for an installer ISO (--linux). Windows 11 ARM64 guests download
official install media and run on the QEMU backend (--from-windows).`,
		Example: `  weave create --from-ipsw latest sequoia
  weave create --from-ipsw ~/Downloads/UniversalMac.ipsw sequoia
  weave create --linux ubuntu
  weave create --from-windows 11 win11 --unattend-file autounattend.xml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, ok := diskimage.ParseDiskImageFormat(diskFormat)
			if !ok {
				return fmt.Errorf("unsupported disk format: %q", diskFormat)
			}
			c.DiskFormat = format
			c.Name = args[0]
			if err := c.Validate(); err != nil {
				return err
			}
			return runBackground(cmd, func(ctx context.Context) error { return c.Run(ctx) })
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&c.FromIPSW, "from-ipsw", "", `IPSW path, URL or "latest" to install macOS from`)
	flags.BoolVar(&c.Linux, "linux", false, "create an empty EFI Linux VM")
	flags.StringVar(&c.FromWindows, "from-windows", "", "create a Windows 11 ARM64 guest (QEMU backend)")
	flags.StringVar(&c.WindowsEdition, "windows-edition", "", `Windows edition label (default "Professional")`)
	flags.StringVar(&c.WindowsConfig, "windows-config", "", "path to a JSON/YAML MediaConfig file for the Windows installer")
	flags.StringVar(&c.UnattendFile, "unattend-file", "", "autounattend.xml to embed so Windows Setup runs unattended")
	flags.StringVar(&c.NetProfile, "net-profile", "", "persist a default network profile (nat|internet-only|isolated|vm-lab|bridged)")
	flags.Uint16Var(&c.DiskSize, "disk-size", 50, "root disk size in GB")
	flags.StringVar(&diskFormat, "disk-format", "raw", "root disk image format (raw|asif)")
	return cmd
}
