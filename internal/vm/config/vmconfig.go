// Port of tart's VMConfig.swift. Swift's Codable conformance becomes
// MarshalJSON/UnmarshalJSON; encoding goes through a map[string]any so the
// standard library emits sorted keys, matching Config.jsonEncoder()'s
// .sortedKeys output. Platform-specific keys are flattened into the same
// object via Platform.platformEncodeJSON, mirroring platform.encode(to:).
//go:build darwin

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/deploymenttheory/guestweave/internal/clipboardpolicy"
	"github.com/deploymenttheory/guestweave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"

	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
)

// lessThanMinimalResourcesError ports VMConfig.swift's class of the same name.
type lessThanMinimalResourcesError struct {
	UserExplanation string
}

func (e *lessThanMinimalResourcesError) Error() string {
	return "LessThanMinimalResourcesError: " + e.UserExplanation
}

// VMDisplayConfigUnit mirrors VMDisplayConfig.Unit.
type VMDisplayConfigUnit string

const (
	VMDisplayConfigUnitPoint VMDisplayConfigUnit = "pt"
	VMDisplayConfigUnitPixel VMDisplayConfigUnit = "px"
)

// VMDisplayConfig mirrors tart's VMDisplayConfig struct.
type VMDisplayConfig struct {
	Width  int                  `json:"width"`
	Height int                  `json:"height"`
	Unit   *VMDisplayConfigUnit `json:"unit,omitempty"`
}

func defaultVMDisplayConfig() VMDisplayConfig {
	return VMDisplayConfig{Width: 1024, Height: 768}
}

// String ports VMDisplayConfig's CustomStringConvertible conformance.
func (d VMDisplayConfig) String() string {
	if d.Unit != nil {
		return fmt.Sprintf("%dx%d%s", d.Width, d.Height, *d.Unit)
	}
	return fmt.Sprintf("%dx%d", d.Width, d.Height)
}

// VMConfig mirrors tart's VMConfig struct. CPUCount and MemorySize are
// private(set) in Swift; mutate them only through SetCPU/SetMemory.
type VMConfig struct {
	Version       int
	OS            weaveplatform.OS
	Arch          weaveplatform.Architecture
	Platform      Platform
	CPUCountMin   int
	CPUCount      int
	MemorySizeMin uint64
	MemorySize    uint64
	MACAddress    *idvirt.MACAddress
	Display       VMDisplayConfig
	DisplayRefit  *bool
	DiskFormat    diskimage.DiskImageFormat
	// NICs is the per-NIC network topology. Empty means "legacy single NAT
	// NIC", synthesised on demand via EnsureNICs from MACAddress. The primary
	// NIC's MAC mirrors MACAddress for backward compatibility.
	NICs []NICConfig
	// ClipboardPolicy optionally overrides the enterprise clipboard policy for
	// this VM. nil means "use the settings default / CLI flags".
	ClipboardPolicy *clipboardpolicy.Policy
	// Windows holds Windows-guest settings; non-nil only when OS == OSWindows.
	// Windows VMs run on the QEMU backend and leave Platform nil.
	Windows *WindowsConfig
}

// WindowsConfig holds the QEMU-backed Windows guest settings persisted in a
// VM's config.json. Firmware vars, the QMP socket and the system disk live in
// the VM directory under fixed names; only the install media and its provenance
// are recorded here.
type WindowsConfig struct {
	// Release is the Windows 11 feature release the install media was built
	// for, e.g. "24H2". Informational/provenance.
	Release string `json:"release,omitempty"`
	// Edition is the Windows edition, e.g. "Professional".
	Edition string `json:"edition,omitempty"`
	// InstallISO is the path to the bootable install ISO (typically cached
	// under ~/.weave/cache/windows/iso). Empty once Windows is installed and
	// the media is detached.
	InstallISO string `json:"installISO,omitempty"`
	// SetupMode controls first-boot automation. "interactive" (default) automates
	// only the UEFI boot steps (via OCR) then hands Windows Setup to the user over
	// VNC; "unattend" drives a fully automated install from an autounattend.xml.
	SetupMode string `json:"setupMode,omitempty"`
	// UnattendFile is the path to a user-supplied autounattend.xml, used when
	// SetupMode is "unattend". It is embedded at the install ISO root (and/or
	// attached as removable media) so Windows Setup runs unattended.
	UnattendFile string `json:"unattendFile,omitempty"`
}

// Windows setup modes for WindowsConfig.SetupMode.
const (
	SetupModeInteractive = "interactive"
	SetupModeUnattend    = "unattend"
)

// EffectiveSetupMode returns the configured setup mode, defaulting to
// "interactive" for an unset/unknown value or a nil receiver.
func (w *WindowsConfig) EffectiveSetupMode() string {
	if w != nil && w.SetupMode == SetupModeUnattend {
		return SetupModeUnattend
	}
	return SetupModeInteractive
}

// NewVMConfig ports VMConfig.init(platform:cpuCountMin:memorySizeMin:
// macAddress:diskFormat:). A nil macAddress selects a random
// locally-administered address, like the Swift default argument.
func NewVMConfig(platform Platform, cpuCountMin int, memorySizeMin uint64, macAddress *idvirt.MACAddress, diskFormat diskimage.DiskImageFormat) *VMConfig {
	if macAddress == nil {
		macAddress = idvirt.RandomLocallyAdministeredAddress()
	}
	return &VMConfig{
		Version:       1,
		OS:            platform.OS(),
		Arch:          weaveplatform.CurrentArchitecture(),
		Platform:      platform,
		CPUCountMin:   cpuCountMin,
		CPUCount:      cpuCountMin,
		MemorySizeMin: memorySizeMin,
		MemorySize:    memorySizeMin,
		MACAddress:    macAddress,
		Display:       defaultVMDisplayConfig(),
		DiskFormat:    diskFormat,
	}
}

// NewVMConfigFromJSON ports VMConfig.init(fromJSON:).
func NewVMConfigFromJSON(data []byte) (*VMConfig, error) {
	config := &VMConfig{}
	if err := json.Unmarshal(data, config); err != nil {
		return nil, err
	}
	return config, nil
}

// NewVMConfigFromURL ports VMConfig.init(fromURL:).
func NewVMConfigFromURL(path string) (*VMConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return NewVMConfigFromJSON(data)
}

// ToJSON ports VMConfig.toJSON(): compact JSON with sorted keys.
func (c *VMConfig) ToJSON() ([]byte, error) {
	return json.Marshal(c)
}

// Save ports VMConfig.save(toURL:): pretty-printed JSON written atomically.
func (c *VMConfig) Save(toPath string) error {
	object, err := c.jsonObject()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically (temp + rename) to match WriteToURLAtomically.
	tmp := toPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return weaveerrors.ErrGeneric("failed to write VM configuration to %s: %v", toPath, err)
	}
	if err := os.Rename(tmp, toPath); err != nil {
		return weaveerrors.ErrGeneric("failed to write VM configuration to %s: %v", toPath, err)
	}
	return nil
}

// SaveInPlace rewrites the config file at toPath without replacing its inode
// (truncate + write rather than temp + rename). Save's atomic rename swaps the
// inode, which would invalidate the fcntl(2) record lock the running VM process
// holds on config.json and make the VM misreport as stopped. A live update from
// inside the running process must use this instead. It is not crash-atomic, so
// reserve it for that case; prefer Save everywhere else.
func (c *VMConfig) SaveInPlace(toPath string) error {
	object, err := c.jsonObject()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(toPath, data, 0o644); err != nil {
		return weaveerrors.ErrGeneric("failed to write VM configuration to %s: %v", toPath, err)
	}
	return nil
}

func (c *VMConfig) jsonObject() (map[string]any, error) {
	object := map[string]any{
		"version":       c.Version,
		"os":            c.OS,
		"arch":          c.Arch,
		"cpuCountMin":   c.CPUCountMin,
		"cpuCount":      c.CPUCount,
		"memorySizeMin": c.MemorySizeMin,
		"memorySize":    c.MemorySize,
		"macAddress":    c.MACAddress.String(),
		"display":       c.Display,
		"diskFormat":    string(c.DiskFormat),
	}
	if c.DisplayRefit != nil {
		object["displayRefit"] = *c.DisplayRefit
	}
	if len(c.NICs) > 0 {
		object["nics"] = c.NICs
	}
	if c.ClipboardPolicy != nil {
		object["clipboardPolicy"] = c.ClipboardPolicy
	}
	if c.Windows != nil {
		object["windows"] = c.Windows
	}
	// Windows guests run on the QEMU backend and carry no VZ Platform; their
	// only platform-specific key is the "windows" object above.
	if c.Platform != nil {
		if err := c.Platform.platformEncodeJSON(object); err != nil {
			return nil, err
		}
	}
	return object, nil
}

// MarshalJSON ports VMConfig.encode(to:).
func (c *VMConfig) MarshalJSON() ([]byte, error) {
	object, err := c.jsonObject()
	if err != nil {
		return nil, err
	}
	return json.Marshal(object)
}

// vmConfigJSON is the decoding container for VMConfig.init(from:), including
// the flattened platform-specific keys.
type vmConfigJSON struct {
	Version         int                         `json:"version"`
	OS              *weaveplatform.OS           `json:"os"`
	Arch            *weaveplatform.Architecture `json:"arch"`
	CPUCountMin     int                         `json:"cpuCountMin"`
	CPUCount        int                         `json:"cpuCount"`
	MemorySizeMin   uint64                      `json:"memorySizeMin"`
	MemorySize      uint64                      `json:"memorySize"`
	MACAddress      string                      `json:"macAddress"`
	Display         *VMDisplayConfig            `json:"display"`
	DisplayRefit    *bool                       `json:"displayRefit"`
	DiskFormat      string                      `json:"diskFormat"`
	NICs            []NICConfig                 `json:"nics"`
	ClipboardPolicy *clipboardpolicy.Policy     `json:"clipboardPolicy"`

	// Windows-guest settings (present only for OS == windows).
	Windows *WindowsConfig `json:"windows"`

	// macOS-specific keys
	ECID          string `json:"ecid"`
	HardwareModel string `json:"hardwareModel"`
}

// UnmarshalJSON ports VMConfig.init(from:).
func (c *VMConfig) UnmarshalJSON(data []byte) error {
	var decoded vmConfigJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	c.Version = decoded.Version

	c.OS = weaveplatform.OSDarwin
	if decoded.OS != nil {
		c.OS = *decoded.OS
	}
	c.Arch = weaveplatform.ArchitectureARM64
	if decoded.Arch != nil {
		c.Arch = *decoded.Arch
	}

	switch c.OS {
	case weaveplatform.OSLinux:
		c.Platform = &LinuxPlatform{}
	case weaveplatform.OSWindows:
		// Windows guests run on the QEMU backend; they carry no VZ Platform.
		c.Platform = nil
		c.Windows = decoded.Windows
	default:
		if runtime.GOARCH != "arm64" {
			return weaveerrors.ErrGeneric("Darwin VMs are only supported on Apple Silicon hosts")
		}
		platform, err := newDarwinPlatformFromJSON(decoded)
		if err != nil {
			return err
		}
		c.Platform = platform
	}

	c.CPUCountMin = decoded.CPUCountMin
	c.CPUCount = decoded.CPUCount
	c.MemorySizeMin = decoded.MemorySizeMin
	c.MemorySize = decoded.MemorySize

	macAddress := idvirt.NewMACAddressWithString(decoded.MACAddress)
	if macAddress == nil {
		return weaveerrors.ErrGeneric("failed to initialize VZMacAddress using the provided value")
	}
	c.MACAddress = macAddress

	// Per-NIC topology. When present it is authoritative; otherwise it stays
	// empty and EnsureNICs synthesises a single primary NAT NIC from
	// MACAddress on demand. Keep MACAddress in sync with the primary NIC so
	// legacy consumers (VMDirectory.MACAddress, IP resolution) keep working.
	c.NICs = decoded.NICs
	if primary := c.PrimaryNIC(); primary != nil && primary.MACAddress != "" {
		primaryMAC := idvirt.NewMACAddressWithString(primary.MACAddress)
		if primaryMAC != nil {
			c.MACAddress = primaryMAC
		}
	}

	c.Display = defaultVMDisplayConfig()
	if decoded.Display != nil {
		c.Display = *decoded.Display
	}
	c.DisplayRefit = decoded.DisplayRefit

	c.DiskFormat = diskimage.DiskImageFormatRaw
	if format, ok := diskimage.ParseDiskImageFormat(decoded.DiskFormat); ok {
		c.DiskFormat = format
	}

	c.ClipboardPolicy = decoded.ClipboardPolicy

	return nil
}

// SetCPU ports VMConfig.setCPU(cpuCount:).
func (c *VMConfig) SetCPU(cpuCount int) error {
	if c.OS == weaveplatform.OSDarwin && cpuCount < c.CPUCountMin {
		return &lessThanMinimalResourcesError{UserExplanation: fmt.Sprintf(
			"VM should have %d CPU cores at minimum (requested %d)", c.CPUCountMin, cpuCount)}
	}

	if minimumAllowed := int(idvirt.MinimumAllowedCPUCount()); cpuCount < minimumAllowed {
		return &lessThanMinimalResourcesError{UserExplanation: fmt.Sprintf(
			"VM should have %d CPU cores at minimum (requested %d)", minimumAllowed, cpuCount)}
	}

	c.CPUCount = cpuCount
	return nil
}

// SetMemory ports VMConfig.setMemory(memorySize:).
func (c *VMConfig) SetMemory(memorySize uint64) error {
	if c.OS == weaveplatform.OSDarwin && memorySize < c.MemorySizeMin {
		return &lessThanMinimalResourcesError{UserExplanation: fmt.Sprintf(
			"VM should have %d bytes of memory at minimum (requested %d)", c.MemorySizeMin, memorySize)}
	}

	if minimumAllowed := idvirt.MinimumAllowedMemorySize(); memorySize < minimumAllowed {
		return &lessThanMinimalResourcesError{UserExplanation: fmt.Sprintf(
			"VM should have %d bytes of memory at minimum (requested %d)", minimumAllowed, memorySize)}
	}

	c.MemorySize = memorySize
	return nil
}
