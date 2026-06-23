// Port of tart's Platform/OS.swift.
//go:build darwin

package platform

// OS mirrors tart's OS enum.
type OS string

const (
	OSDarwin OS = "darwin"
	OSLinux  OS = "linux"
	// OSWindows is a Windows guest. Unlike Darwin/Linux (which boot on Apple's
	// Virtualization.framework), Windows guests run on the QEMU backend, since
	// VZ cannot boot Windows.
	OSWindows OS = "windows"
)
