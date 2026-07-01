// Port of tart's Platform/Platform.swift. The Codable conformance becomes
// the platformEncodeJSON hook, which flattens platform-specific keys into
// the VMConfig JSON object exactly like the Swift encode(to:) overloads.
//go:build darwin

package vmconfig

import (
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
)

// Platform ports tart's Platform protocol. It returns idiomatic provider
// interfaces so the VM-config assembly stays raw-free; the path arguments are
// plain filesystem paths (the idiomatic constructors take string URLs).
type Platform interface {
	OS() weaveplatform.OS
	BootLoader(nvramPath string) (virtualization.BootLoaderProvider, error)
	Platform(nvramPath string, needsNestedVirtualization bool) (virtualization.PlatformConfigurationProvider, error)
	GraphicsDevice(vmConfig *VMConfig) virtualization.GraphicsDeviceConfigurationProvider
	Keyboards() []virtualization.KeyboardConfigurationProvider
	PointingDevices() []virtualization.PointingDeviceConfigurationProvider
	PointingDevicesSimplified() []virtualization.PointingDeviceConfigurationProvider

	// platformEncodeJSON adds the platform-specific keys (e.g. Darwin's
	// ecid/hardwareModel) to the VMConfig JSON object.
	platformEncodeJSON(object map[string]any) error
}

// PlatformSuspendable ports tart's PlatformSuspendable protocol.
type PlatformSuspendable interface {
	Platform
	PointingDevicesSuspendable() []virtualization.PointingDeviceConfigurationProvider
	KeyboardsSuspendable() []virtualization.KeyboardConfigurationProvider
}
