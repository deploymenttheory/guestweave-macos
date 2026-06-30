// Port of tart's Commands/Set.swift.
//go:build darwin

package command

import (
	"context"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/deploymenttheory/weave/internal/vmconfig"

	"github.com/deploymenttheory/weave/internal/clipboardpolicy"
	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/diskimage"
	"github.com/deploymenttheory/weave/internal/fsutil"
	"github.com/deploymenttheory/weave/internal/vmstorage"

	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
)

// SetCommand ports the Set command.
type SetCommand struct {
	Name         string
	CPU          *uint16
	Memory       *uint64
	Display      *vmconfig.VMDisplayConfig
	DisplayRefit *bool
	RandomMAC    bool
	RandomSerial bool
	Disk         string
	DiskSize     *uint16
	Clipboard    clipboardFlagValues // persisted onto the VM's clipboard policy
}

func (c *SetCommand) Run(ctx context.Context) error {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(c.Name)
	if err != nil {
		return err
	}
	vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return err
	}

	if c.CPU != nil {
		if err := vmConfig.SetCPU(int(*c.CPU)); err != nil {
			return err
		}
	}

	if c.Memory != nil {
		if err := vmConfig.SetMemory(*c.Memory * 1024 * 1024); err != nil {
			return err
		}
	}

	if c.Display != nil {
		if c.Display.Width > 0 {
			vmConfig.Display.Width = c.Display.Width
		}
		if c.Display.Height > 0 {
			vmConfig.Display.Height = c.Display.Height
		}
		vmConfig.Display.Unit = c.Display.Unit
	}

	vmConfig.DisplayRefit = c.DisplayRefit

	if c.RandomMAC {
		vmConfig.MACAddress = idvirt.RandomLocallyAdministeredAddress()
	}

	if c.RandomSerial && runtime.GOARCH == "arm64" {
		if oldPlatform, ok := vmConfig.Platform.(*vmconfig.DarwinPlatform); ok {
			ecid := idvirt.NewMacMachineIdentifier()
			vmConfig.Platform = vmconfig.NewDarwinPlatform(ecid, oldPlatform.HardwareModel)
		}
	}

	// Persist any clipboard-policy flags onto the VM's stored policy, layering
	// the override on the existing per-VM policy (or the built-in default).
	if override := c.Clipboard.override(); !override.IsZero() {
		base := clipboardpolicy.Default()
		if vmConfig.ClipboardPolicy != nil {
			base = *vmConfig.ClipboardPolicy
		}
		updated := override.Apply(base)
		vmConfig.ClipboardPolicy = &updated
	}

	if err := vmConfig.Save(vmDir.ConfigURL()); err != nil {
		return err
	}

	if c.Disk != "" {
		config, err := weaveconfig.NewConfig()
		if err != nil {
			return err
		}
		temporaryDiskPath := filepath.Join(config.WeaveTmpDir, "set-disk-"+fsutil.UUID())

		if err := fsutil.CopyItem(c.Disk, temporaryDiskPath); err != nil {
			return err
		}
		if err := vmstorage.FileManagerReplaceItem(vmDir.DiskURL(), temporaryDiskPath); err != nil {
			return err
		}
	}

	if c.DiskSize != nil {
		return vmDir.ResizeDisk(*c.DiskSize, diskimage.DiskImageFormatRaw)
	}

	return nil
}

// ParseVMDisplayConfig ports the VMDisplayConfig ExpressibleByArgument
// conformance: WIDTHxHEIGHT with an optional pt/px suffix.
func ParseVMDisplayConfig(argument string) vmconfig.VMDisplayConfig {
	var unit *vmconfig.VMDisplayConfigUnit

	if strings.HasSuffix(argument, string(vmconfig.VMDisplayConfigUnitPixel)) {
		argument = strings.TrimSuffix(argument, string(vmconfig.VMDisplayConfigUnitPixel))
		pixel := vmconfig.VMDisplayConfigUnitPixel
		unit = &pixel
	} else if strings.HasSuffix(argument, string(vmconfig.VMDisplayConfigUnitPoint)) {
		argument = strings.TrimSuffix(argument, string(vmconfig.VMDisplayConfigUnitPoint))
		point := vmconfig.VMDisplayConfigUnitPoint
		unit = &point
	}

	parts := strings.Split(argument, "x")
	config := vmconfig.VMDisplayConfig{Unit: unit}
	if len(parts) > 0 {
		config.Width, _ = strconv.Atoi(parts[0])
	}
	if len(parts) > 1 {
		config.Height, _ = strconv.Atoi(parts[1])
	}
	return config
}
