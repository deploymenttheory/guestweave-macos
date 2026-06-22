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

var _ Platform = (*LinuxPlatform)(nil)

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
