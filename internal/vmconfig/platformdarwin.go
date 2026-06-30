// Port of tart's Platform/Darwin.swift: the macOS guest platform. Idiomatic
// wrappers provide the constructors and fluent setters.
//go:build darwin

package vmconfig

import (
	"encoding/base64"

	weaveplatform "github.com/deploymenttheory/weave/internal/platform"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"

	appkit "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/appkit"
	corefoundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/corefoundation"
	idiomatic "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
)

// UnsupportedHostOSError ports Darwin.swift's UnsupportedHostOSError.
type UnsupportedHostOSError struct{}

func (UnsupportedHostOSError) Error() string {
	return "error: host macOS version is outdated to run this virtual machine"
}

// DarwinPlatform ports tart's Darwin struct (named to avoid clashing with
// the weaveplatform.OS constant).
type DarwinPlatform struct {
	ECID          *idiomatic.MacMachineIdentifier
	HardwareModel *idiomatic.MacHardwareModel
}

var (
	_ Platform            = (*DarwinPlatform)(nil)
	_ PlatformSuspendable = (*DarwinPlatform)(nil)
)

// NewDarwinPlatform ports Darwin.init(ecid:hardwareModel:).
func NewDarwinPlatform(ecid *idiomatic.MacMachineIdentifier, hardwareModel *idiomatic.MacHardwareModel) *DarwinPlatform {
	return &DarwinPlatform{ECID: ecid, HardwareModel: hardwareModel}
}

// newDarwinPlatformFromJSON ports Darwin.init(from:): decodes the
// base64-encoded ecid and hardwareModel keys.
func newDarwinPlatformFromJSON(config vmConfigJSON) (*DarwinPlatform, error) {
	ecidData, err := base64.StdEncoding.DecodeString(config.ECID)
	if err != nil {
		return nil, weaveerrors.ErrGeneric("failed to initialize Data using the provided value")
	}
	ecid := idiomatic.NewMACMachineIdentifierWithDataRepresentation(objcutil.BytesToNSData(ecidData))
	if ecid == nil {
		return nil, weaveerrors.ErrGeneric("failed to initialize VZMacMachineIdentifier using the provided value")
	}

	hardwareModelData, err := base64.StdEncoding.DecodeString(config.HardwareModel)
	if err != nil {
		return nil, weaveerrors.ErrGeneric("failed to initialize Data using the provided value")
	}
	hardwareModel := idiomatic.NewMACHardwareModelWithDataRepresentation(objcutil.BytesToNSData(hardwareModelData))
	if hardwareModel == nil {
		return nil, UnsupportedHostOSError{}
	}

	return &DarwinPlatform{ECID: ecid, HardwareModel: hardwareModel}, nil
}

func (p *DarwinPlatform) platformEncodeJSON(object map[string]any) error {
	object["ecid"] = base64.StdEncoding.EncodeToString(obj.Bytes(p.ECID.DataRepresentation()))
	object["hardwareModel"] = base64.StdEncoding.EncodeToString(obj.Bytes(p.HardwareModel.DataRepresentation()))
	return nil
}

func (p *DarwinPlatform) OS() weaveplatform.OS { return weaveplatform.OSDarwin }

// Serial returns the VM's machine identifier (ECID) as a base64 string — the
// value `--random-serial` regenerates and the basis of the guest's hardware
// identity. Returns "" for a guest with no macOS platform (e.g. Linux).
func (c *VMConfig) Serial() string {
	p, ok := c.Platform.(*DarwinPlatform)
	if !ok || p.ECID == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(obj.Bytes(p.ECID.DataRepresentation()))
}

func (p *DarwinPlatform) BootLoader(nvramPath string) (idiomatic.BootLoaderProvider, error) {
	return idiomatic.NewMacOSBootLoader(), nil
}

func (p *DarwinPlatform) Platform(nvramPath string, needsNestedVirtualization bool) (idiomatic.PlatformConfigurationProvider, error) {
	if needsNestedVirtualization {
		return nil, weaveerrors.ErrVMConfigurationError("macOS virtual machines do not support nested virtualization")
	}

	if !p.HardwareModel.IsSupported() {
		// At the moment support of the M1 chip is not yet dropped in any
		// macOS version, which means the host software does not support
		// this hardware model and should be updated.
		return nil, UnsupportedHostOSError{}
	}

	return idiomatic.NewMacPlatformConfiguration().
		WithMachineIdentifier(p.ECID).
		WithAuxiliaryStorage(idiomatic.NewMACAuxiliaryStorageWithURL(nvramPath)).
		WithHardwareModel(p.HardwareModel), nil
}

func (p *DarwinPlatform) GraphicsDevice(vmConfig *VMConfig) idiomatic.GraphicsDeviceConfigurationProvider {
	unit := VMDisplayConfigUnitPoint
	if vmConfig.Display.Unit != nil {
		unit = *vmConfig.Display.Unit
	}
	if hostMainScreen := appkit.MainScreen(); unit == VMDisplayConfigUnitPoint && hostMainScreen != nil {
		vmScreenSize := corefoundation.CGSize{
			Width:  float64(vmConfig.Display.Width),
			Height: float64(vmConfig.Display.Height),
		}
		display := idiomatic.NewMACGraphicsDisplayConfigurationForScreenSizeInPoints(hostMainScreen, vmScreenSize)
		return idiomatic.NewMacGraphicsDeviceConfiguration().WithDisplays(display)
	}

	// 72 PPI is a reasonable guess according to Apple's CGDisplayScreenSize
	// documentation.
	display := idiomatic.NewMACGraphicsDisplayConfigurationWithWidthInPixelsHeightInPixelsPixelsPerInch(
		vmConfig.Display.Width, vmConfig.Display.Height, 72)
	return idiomatic.NewMacGraphicsDeviceConfiguration().WithDisplays(display)
}

func (p *DarwinPlatform) Keyboards() []idiomatic.KeyboardConfigurationProvider {
	// The Mac keyboard is only supported by guests starting with macOS
	// Ventura; tart gates it on the host running macOS 14.
	if weaveplatform.MacOSAtLeast(14) {
		return []idiomatic.KeyboardConfigurationProvider{
			idiomatic.NewUSBKeyboardConfiguration(),
			idiomatic.NewMacKeyboardConfiguration(),
		}
	}
	return []idiomatic.KeyboardConfigurationProvider{
		idiomatic.NewUSBKeyboardConfiguration(),
	}
}

func (p *DarwinPlatform) KeyboardsSuspendable() []idiomatic.KeyboardConfigurationProvider {
	if weaveplatform.MacOSAtLeast(14) {
		return []idiomatic.KeyboardConfigurationProvider{
			idiomatic.NewMacKeyboardConfiguration(),
		}
	}
	return p.Keyboards()
}

func (p *DarwinPlatform) PointingDevices() []idiomatic.PointingDeviceConfigurationProvider {
	// The trackpad is only supported by guests starting with macOS Ventura.
	return []idiomatic.PointingDeviceConfigurationProvider{
		idiomatic.NewUSBScreenCoordinatePointingDeviceConfiguration(),
		idiomatic.NewMacTrackpadConfiguration(),
	}
}

func (p *DarwinPlatform) PointingDevicesSimplified() []idiomatic.PointingDeviceConfigurationProvider {
	// Only include the USB pointing device, not the trackpad.
	return []idiomatic.PointingDeviceConfigurationProvider{
		idiomatic.NewUSBScreenCoordinatePointingDeviceConfiguration(),
	}
}

func (p *DarwinPlatform) PointingDevicesSuspendable() []idiomatic.PointingDeviceConfigurationProvider {
	if weaveplatform.MacOSAtLeast(14) {
		return []idiomatic.PointingDeviceConfigurationProvider{
			idiomatic.NewMacTrackpadConfiguration(),
		}
	}
	return p.PointingDevices()
}
