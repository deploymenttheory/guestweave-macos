//go:build darwin

package winimage

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// ISO9660 layout constants. The first 16 logical sectors (16 * 2048 = 32768
// bytes) are the System Area; the Primary Volume Descriptor (PVD) begins at
// sector 16. Within the PVD, byte 0 is the descriptor type (1 = primary),
// bytes 1..5 are the standard identifier "CD001", and bytes 40..71 hold the
// 32-byte, space-padded Volume Identifier.
const (
	isoPVDOffset   = 16 * 2048 // byte offset of the Primary Volume Descriptor
	isoVolIDOffset = 40        // volume-identifier offset within the PVD
	isoVolIDLen    = 32        // volume-identifier length
	isoStdID       = "CD001"   // standard identifier at PVD bytes 1..5
	isoPVDType     = 1         // Primary Volume Descriptor type byte
	isoReadLen     = isoVolIDOffset + isoVolIDLen
)

// Architecture classifications returned by InspectISOArch.
const (
	archARM64   = "ARM64"
	archX64     = "x64"
	archUnknown = "unknown"
)

// InspectISOArch reads the ISO9660 Primary Volume Descriptor of the file at
// path and classifies its architecture from the volume identifier. Microsoft
// Windows install media labels ARM64 media "CCCOMA_ARM64FRE" and x64 media
// "CCCOMA_X64FRE", so the volume id is a definitive, dependency-free signal.
//
// It returns archARM64, archX64, or archUnknown. An error is returned only when
// the file cannot be read or is not a valid ISO9660 image.
func InspectISOArch(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("winimage: open ISO: %w", err)
	}
	defer f.Close()

	buf := make([]byte, isoReadLen)
	if _, err := io.ReadFull(io.NewSectionReader(f, isoPVDOffset, isoReadLen), buf); err != nil {
		return "", fmt.Errorf("winimage: read ISO volume descriptor: %w", err)
	}

	if buf[0] != isoPVDType || string(buf[1:6]) != isoStdID {
		return "", fmt.Errorf("winimage: %s is not a valid ISO9660 image (missing %q primary volume descriptor)", path, isoStdID)
	}

	volID := strings.ToUpper(strings.TrimSpace(string(buf[isoVolIDOffset : isoVolIDOffset+isoVolIDLen])))
	switch {
	// Microsoft's official software-download ISOs use CCCOMA_A64FRE_<LANG>_DV9;
	// MCT/ESD-built media uses CCCOMA_ARM64FRE. Both tokens unambiguously
	// identify ARM64 media (the SDK's ArchFromToken uses the same set).
	case strings.Contains(volID, "ARM64"), strings.Contains(volID, "A64"):
		return archARM64, nil
	case strings.Contains(volID, "X64"), strings.Contains(volID, "AMD64"):
		return archX64, nil
	default:
		return archUnknown, nil
	}
}

// RequireARM64ISO verifies that the ISO at path is ARM64 Windows install media,
// returning an actionable error otherwise. It is the single guard every install
// ISO (built, cached, or user-supplied) passes through before reaching QEMU.
func RequireARM64ISO(path string) error {
	arch, err := InspectISOArch(path)
	if err != nil {
		return err
	}
	switch arch {
	case archARM64:
		return nil
	case archX64:
		return fmt.Errorf(
			"%s is an x64 (Intel/AMD) Windows ISO; Windows guests run on ARM64 only. "+
				"Please use an ARM64 (%s) image.", path, arm64VolumeID)
	default:
		return fmt.Errorf(
			"%s does not look like ARM64 Windows install media (its ISO volume id is not %s). "+
				"Windows guests run on ARM64 only.", path, arm64VolumeID)
	}
}
