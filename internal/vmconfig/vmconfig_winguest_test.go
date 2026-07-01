//go:build darwin

package vmconfig

import (
	"testing"

	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"

	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
)

// TestWindowsConfigRoundTrip verifies a Windows VMConfig (nil VZ Platform, with
// a Windows sub-struct) marshals and unmarshals without losing fields and
// without dereferencing the absent Platform.
func TestWindowsConfigRoundTrip(t *testing.T) {
	orig := &VMConfig{
		Version:    1,
		OS:         weaveplatform.OSWindows,
		Arch:       weaveplatform.ArchitectureARM64,
		Platform:   nil,
		CPUCount:   4,
		MemorySize: 4 * 1024 * 1024 * 1024,
		MACAddress: idvirt.RandomLocallyAdministeredAddress(),
		Display:    VMDisplayConfig{Width: 1280, Height: 800},
		Windows: &WindowsConfig{
			Release:    "24H2",
			Edition:    "Professional",
			InstallISO: "/cache/windows/iso/26100-arm64-professional.iso",
		},
	}

	data, err := orig.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}

	got, err := NewVMConfigFromJSON(data)
	if err != nil {
		t.Fatalf("NewVMConfigFromJSON: %v", err)
	}

	if got.OS != weaveplatform.OSWindows {
		t.Errorf("OS = %q, want windows", got.OS)
	}
	if got.Platform != nil {
		t.Errorf("Platform = %v, want nil for Windows", got.Platform)
	}
	if got.Windows == nil {
		t.Fatal("Windows config lost on round-trip")
	}
	if got.Windows.Release != "24H2" || got.Windows.Edition != "Professional" {
		t.Errorf("Windows = %+v, want release 24H2 / Professional", got.Windows)
	}
	if got.Windows.InstallISO != orig.Windows.InstallISO {
		t.Errorf("InstallISO = %q, want %q", got.Windows.InstallISO, orig.Windows.InstallISO)
	}
	if got.CPUCount != 4 || got.MemorySize != orig.MemorySize {
		t.Errorf("resources = %d cpu / %d mem, want 4 / %d", got.CPUCount, got.MemorySize, orig.MemorySize)
	}
}
