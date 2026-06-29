// Port of tart's Platform/Linux.swift: the Linux guest platform.
//go:build darwin

package vmconfig

import (
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
)

// LinuxPlatform ports tart's Linux struct (named to avoid clashing with the
// weaveplatform.OS constant).
type LinuxPlatform struct{}

var (
	_ Platform            = (*LinuxPlatform)(nil)
	_ PlatformSuspendable = (*LinuxPlatform)(nil)
)

func (p *LinuxPlatform) platformEncodeJSON(object map[string]any) error {
	// Linux contributes no platform-specific keys.
	return nil
}

func (p *LinuxPlatform) OS() weaveplatform.OS { return weaveplatform.OSLinux }

func (p *LinuxPlatform) BootLoader(nvramPath string) (virtualization.BootLoaderProvider, error) {
	return virtualization.NewEFIBootLoader().
		WithVariableStore(virtualization.NewEFIVariableStoreWithURL(nvramPath)), nil
}

func (p *LinuxPlatform) Platform(nvramPath string, needsNestedVirtualization bool) (virtualization.PlatformConfigurationProvider, error) {
	config := virtualization.NewGenericPlatformConfiguration()
	if weaveplatform.MacOSAtLeast(15) {
		config.WithNestedVirtualizationEnabled(needsNestedVirtualization)
	}
	return config, nil
}

func (p *LinuxPlatform) GraphicsDevice(vmConfig *VMConfig) virtualization.GraphicsDeviceConfigurationProvider {
	scanout := virtualization.NewVirtioGraphicsScanoutConfigurationWithWidthInPixelsHeightInPixels(
		vmConfig.Display.Width, vmConfig.Display.Height)
	return virtualization.NewVirtioGraphicsDeviceConfiguration().WithScanouts(scanout)
}

func (p *LinuxPlatform) Keyboards() []virtualization.KeyboardConfigurationProvider {
	return []virtualization.KeyboardConfigurationProvider{
		virtualization.NewUSBKeyboardConfiguration(),
	}
}

func (p *LinuxPlatform) PointingDevices() []virtualization.PointingDeviceConfigurationProvider {
	return []virtualization.PointingDeviceConfigurationProvider{
		virtualization.NewUSBScreenCoordinatePointingDeviceConfiguration(),
	}
}

func (p *LinuxPlatform) PointingDevicesSimplified() []virtualization.PointingDeviceConfigurationProvider {
	// Linux doesn't support the trackpad, so just return the regular
	// pointing devices.
	return p.PointingDevices()
}

// KeyboardsSuspendable returns the keyboards to attach when the VM runs in
// suspendable mode. The Virtualization framework's save/restore (macOS 14+)
// rejects USB input devices, and the save-restore-capable VZMacKeyboard is a
// macOS-guest-only device, so a suspendable Linux guest runs with no keyboard.
// Interact with it over SSH/VNC while suspendable; the empty set keeps the
// configuration save/restore-compatible (see vm.AssembleConfiguration, which
// skips the device setter for an empty slice).
func (p *LinuxPlatform) KeyboardsSuspendable() []virtualization.KeyboardConfigurationProvider {
	return nil
}

// PointingDevicesSuspendable mirrors KeyboardsSuspendable: USB pointing devices
// are not save/restore-compatible and the VZMacTrackpad is macOS-guest-only, so
// a suspendable Linux guest runs without a pointing device.
func (p *LinuxPlatform) PointingDevicesSuspendable() []virtualization.PointingDeviceConfigurationProvider {
	return nil
}
