// Port of tart's Commands/Create.swift.
//go:build darwin

package command

import (
	"context"
	"os"
	"runtime"
	"strings"

	"github.com/deploymenttheory/guestweave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	weavelock "github.com/deploymenttheory/guestweave/internal/lock"
	weavenetwork "github.com/deploymenttheory/guestweave/internal/network"
	"github.com/deploymenttheory/guestweave/internal/objcutil"
	"github.com/deploymenttheory/guestweave/internal/terminal"
	weavevm "github.com/deploymenttheory/guestweave/internal/vm"
	"github.com/deploymenttheory/guestweave/internal/vmconfig"
	"github.com/deploymenttheory/guestweave/internal/vmdirectory"
	"github.com/deploymenttheory/guestweave/internal/vmstorage"

	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
)

// CreateCommand ports the Create command.
type CreateCommand struct {
	Name     string
	FromIPSW string
	Linux    bool
	// FromWindows creates a Windows 11 ARM64 guest. The install media is
	// downloaded from Microsoft's software-download site (latest official
	// ARM64 ISO) and the VM runs on the QEMU backend. The value is used as a
	// label only; pass any non-empty string (e.g. "--from-windows").
	FromWindows string
	// WindowsEdition selects the edition label for display (default
	// "Professional"); only meaningful with FromWindows.
	WindowsEdition string
	// WindowsConfig is an optional path to a JSON or YAML MediaConfig file
	// that specifies edition, language, and/or an unattend_file.
	// CLI flags (--windows-edition, --unattend-file) override config-file values.
	WindowsConfig string
	// UnattendFile is an optional path to an autounattend.xml to embed at the
	// ISO root so Windows Setup runs unattended.
	UnattendFile string
	DiskSize     uint16
	DiskFormat   diskimage.DiskImageFormat
	// NetProfile optionally persists a default network profile into the new
	// VM's config (nat|internet-only|isolated|vm-lab|bridged). Empty leaves
	// the VM on the implicit single NAT NIC, overridable at run time.
	NetProfile string
}

func (c *CreateCommand) Validate() error {
	if c.FromIPSW == "" && !c.Linux && c.FromWindows == "" {
		return weaveerrors.ErrGeneric("Please specify one of --from-ipsw, --linux or --from-windows!")
	}
	if c.FromWindows != "" && (c.FromIPSW != "" || c.Linux) {
		return weaveerrors.ErrGeneric("--from-windows cannot be combined with --from-ipsw or --linux")
	}
	if runtime.GOARCH != "arm64" && c.FromIPSW != "" {
		return weaveerrors.ErrGeneric("Only Linux VMs are supported on Intel!")
	}
	if c.FromWindows != "" && runtime.GOARCH != "arm64" {
		return weaveerrors.ErrGeneric("Windows 11 ARM64 guests require an Apple Silicon host")
	}

	// Validate disk format support.
	if !c.DiskFormat.IsSupported() {
		return weaveerrors.ErrGeneric("Disk format '%s' is not supported on this system.", c.DiskFormat)
	}

	// Validate the network profile name up front.
	if c.NetProfile != "" {
		if _, err := weavenetwork.ExpandProfile(c.NetProfile, weavenetwork.ProfileOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func (c *CreateCommand) Run(ctx context.Context) error {
	tmpVMDir, err := vmdirectory.VMDirectoryTemporary()
	if err != nil {
		return err
	}

	// Lock the temporary VM directory to prevent its garbage collection.
	tmpVMDirLock, err := weavelock.NewFileLock(tmpVMDir.BaseURL)
	if err != nil {
		return err
	}
	defer tmpVMDirLock.Close()
	if err := tmpVMDirLock.Lock(); err != nil {
		return err
	}

	cleanup := func() {
		_ = os.RemoveAll(tmpVMDir.BaseURL)
	}

	if c.FromIPSW != "" {
		var ipswLocation string

		switch {
		case c.FromIPSW == "latest":
			spinner := terminal.NewSpinner("Looking up the latest supported IPSW")
			spinner.Start()
			image, err := FetchLatestSupportedRestoreImage(ctx)
			if err != nil {
				spinner.Fail("Failed to look up the latest supported IPSW")
				cleanup()
				return err
			}
			spinner.Success("Found the latest supported IPSW")
			ipswLocation = absoluteURLString(image.URL())
		case strings.HasPrefix(c.FromIPSW, "http://") || strings.HasPrefix(c.FromIPSW, "https://"):
			ipswLocation = c.FromIPSW
		default:
			ipswLocation = objcutil.ExpandTilde(c.FromIPSW)
		}

		if _, err := weavevm.NewVMInstallingFromIPSW(ctx, tmpVMDir, ipswLocation, c.DiskSize, c.DiskFormat, weavevm.VMOptions{}); err != nil {
			cleanup()
			return err
		}
	}

	if c.Linux {
		if _, err := weavevm.VMLinux(tmpVMDir, c.DiskSize, c.DiskFormat); err != nil {
			cleanup()
			return err
		}
	}

	if c.FromWindows != "" {
		if err := c.createWindows(ctx, tmpVMDir); err != nil {
			cleanup()
			return err
		}
	}

	if err := c.persistNetworkProfile(tmpVMDir); err != nil {
		cleanup()
		return err
	}

	localStorage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		cleanup()
		return err
	}
	if err := localStorage.Move(c.Name, tmpVMDir); err != nil {
		cleanup()
		return err
	}

	return nil
}

// persistNetworkProfile writes the chosen --net-profile into the new VM's
// config as a persisted NIC topology. The primary NIC inherits the config's
// MAC; any secondary NICs get deterministic derived MACs. A no-op when no
// profile was requested.
func (c *CreateCommand) persistNetworkProfile(vmDir *vmdirectory.VMDirectory) error {
	if c.NetProfile == "" {
		return nil
	}

	config, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return err
	}

	nics, err := weavenetwork.ExpandProfile(c.NetProfile, weavenetwork.ProfileOptions{})
	if err != nil {
		return err
	}

	primaryMAC := config.MACAddress.String()
	for i := range nics {
		switch {
		case nics[i].IsPrimary:
			nics[i].MACAddress = primaryMAC
		case nics[i].MACAddress == "":
			nics[i].MACAddress = vmconfig.DeriveMACAddress(primaryMAC, i)
		}
	}

	config.NICs = nics
	return config.Save(vmDir.ConfigURL())
}

// FetchLatestSupportedRestoreImage fetches the latest supported macOS restore
// image, blocking until it resolves or ctx is cancelled.
func FetchLatestSupportedRestoreImage(ctx context.Context) (*idvirt.MacOSRestoreImage, error) {
	return idvirt.FetchLatestSupported(ctx)
}
