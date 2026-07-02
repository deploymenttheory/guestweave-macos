//go:build darwin

// Windows-guest creation for `weave create --from-windows`. Unlike the
// VZ-backed macOS/Linux paths, this acquires Windows 11 ARM64 install media
// via the winmediafoundry SDK (internal/winimage), provisions a qcow2 system
// disk with qemu-img, and writes a Windows VMConfig that the run command boots
// on the QEMU backend.

package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
	"github.com/deploymenttheory/guestweave/internal/qemu"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
	"github.com/deploymenttheory/guestweave/internal/winimage"

	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
)

const (
	// windowsDefaultCPUs / windowsDefaultMemory are the starting resources for a
	// Windows 11 ARM64 guest (Win11 requires ≥2 cores / 4 GiB); adjust later
	// with `weave set`.
	windowsDefaultCPUs   = 4
	windowsDefaultMemory = uint64(4) * 1024 * 1024 * 1024
)

// createWindows acquires Windows install media, creates the system disk and
// writes the VM config into vmDir.
func (c *CreateCommand) createWindows(ctx context.Context, vmDir *layout.VMDirectory) error {
	cfg, err := weaveconfig.NewConfig()
	if err != nil {
		return err
	}

	// Build the acquire options, starting from the config file if given.
	opts := winimage.Options{
		CacheDir: filepath.Join(cfg.WeaveCacheDir, "windows"),
		Progress: os.Stdout,
	}

	if c.WindowsConfig != "" {
		mc, err := winimage.LoadMediaConfig(c.WindowsConfig)
		if err != nil {
			return err
		}
		opts.Edition = mc.Edition
		opts.Language = mc.Language
		if mc.UnattendFile != "" {
			opts.Unattend, err = os.ReadFile(mc.UnattendFile)
			if err != nil {
				return fmt.Errorf("read unattend_file from config: %w", err)
			}
		}
	}

	// CLI flags override config-file values.
	if c.WindowsEdition != "" {
		opts.Edition = c.WindowsEdition
	}
	if c.UnattendFile != "" {
		opts.Unattend, err = os.ReadFile(c.UnattendFile)
		if err != nil {
			return fmt.Errorf("read unattend file: %w", err)
		}
	}

	media, err := winimage.Acquire(ctx, opts)
	if err != nil {
		return err
	}

	// Provision the qcow2 system disk with qemu-img.
	tc, err := qemu.ResolveToolchain(cfg.WeaveCacheDir)
	if err != nil {
		return err
	}
	diskSize := uint64(c.DiskSize)
	if diskSize == 0 {
		diskSize = 64
	}
	if err := qemu.CreateDisk(tc.Img, vmDir.DiskURL(), diskSize); err != nil {
		return err
	}

	// Write the Windows VMConfig. Platform is nil — Windows runs on QEMU, not
	// Virtualization.framework.
	conf := &vmconfig.VMConfig{
		Version:       1,
		OS:            weaveplatform.OSWindows,
		Arch:          weaveplatform.ArchitectureARM64,
		Platform:      nil,
		CPUCountMin:   1,
		CPUCount:      windowsDefaultCPUs,
		MemorySizeMin: windowsDefaultMemory,
		MemorySize:    windowsDefaultMemory,
		MACAddress:    idvirt.RandomLocallyAdministeredAddress(),
		Display:       defaultWindowsDisplay(),
		Windows: &vmconfig.WindowsConfig{
			Release:    media.Release,
			Edition:    media.Edition,
			InstallISO: media.ISOPath,
		},
	}
	if err := conf.Save(vmDir.ConfigURL()); err != nil {
		return err
	}

	fmt.Printf("Created Windows %s (%s). Run it with: weave run %s\n",
		media.Release, media.Edition, c.Name)
	return nil
}

// defaultWindowsDisplay is a sensible default resolution for the guest.
func defaultWindowsDisplay() vmconfig.VMDisplayConfig {
	return vmconfig.VMDisplayConfig{Width: 1280, Height: 800}
}
