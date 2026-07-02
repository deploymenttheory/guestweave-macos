// Package qemu is weave's second virtualization backend. Apple's
// Virtualization.framework (the VZ path in internal/vm) cannot boot Windows, so
// Windows 11 ARM64 guests run on qemu-system-aarch64 with HVF acceleration.
// This file resolves the QEMU toolchain (emulator, qemu-img and the edk2 ARM
// UEFI firmware); args.go assembles the command line and process.go owns the
// lifecycle.
//go:build darwin

package qemu

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
)

// Toolchain is the resolved set of QEMU artifacts needed to boot an ARM64 guest.
type Toolchain struct {
	// SystemAARCH64 is the path to qemu-system-aarch64.
	SystemAARCH64 string
	// Img is the path to qemu-img (used to create the system disk).
	Img string
	// FirmwareCode is the read-only edk2 ARM UEFI code image (pflash unit 0).
	FirmwareCode string
	// FirmwareVarsTemplate is the edk2 ARM UEFI variable-store template; a
	// writable per-VM copy is made for pflash unit 1.
	FirmwareVarsTemplate string
	// Accel is the QEMU accelerator: "hvf" on Apple Silicon, else "tcg".
	Accel string
}

// homebrewPrefixes are the standard Homebrew roots searched for a system QEMU.
var homebrewPrefixes = []string{"/opt/homebrew", "/usr/local"}

// firmwareCodeNames are candidate filenames for the edk2 ARM UEFI code image,
// across QEMU/Homebrew versions.
var firmwareCodeNames = []string{"edk2-aarch64-code.fd", "AAVMF_CODE.fd", "QEMU_EFI.fd"}

// firmwareVarsNames are candidate filenames for the edk2 ARM UEFI vars template.
// Homebrew QEMU ships it as edk2-arm-vars.fd; upstream and other builds use
// edk2-aarch64-vars.fd.  Both are a blank NVRAM suitable for AArch64 guests.
var firmwareVarsNames = []string{"edk2-aarch64-vars.fd", "edk2-arm-vars.fd", "AAVMF_VARS.fd"}

// ResolveToolchain locates the QEMU toolchain. Resolution order:
//
//  1. Explicit overrides (GUESTWEAVE_QEMU_SYSTEM_AARCH64, GUESTWEAVE_QEMU_IMG,
//     GUESTWEAVE_QEMU_FIRMWARE_CODE, GUESTWEAVE_QEMU_FIRMWARE_VARS).
//  2. A system install on PATH / under a Homebrew prefix (e.g. `brew install
//     qemu`), with firmware from the matching share/qemu directory.
//
// cacheDir is reserved for the auto-download path (a pinned QEMU build placed
// under <cacheDir>/qemu); see ensureDownloaded.
func ResolveToolchain(cacheDir string) (*Toolchain, error) {
	tc := &Toolchain{Accel: accelerator()}

	tc.SystemAARCH64 = firstExisting(
		weaveconfig.QEMUSystemAarch64(),
		lookPath("qemu-system-aarch64"),
		underHomebrew("bin", "qemu-system-aarch64"),
	)
	tc.Img = firstExisting(
		weaveconfig.QEMUImg(),
		lookPath("qemu-img"),
		underHomebrew("bin", "qemu-img"),
	)
	tc.FirmwareCode = firstExisting(append(
		[]string{weaveconfig.QEMUFirmwareCode()},
		firmwareCandidates(firmwareCodeNames)...,
	)...)
	tc.FirmwareVarsTemplate = firstExisting(append(
		[]string{weaveconfig.QEMUFirmwareVars()},
		firmwareCandidates(firmwareVarsNames)...,
	)...)

	// Windows 11's required TPM 2.0 is provided by the in-process Go-native vTPM
	// (go-sdk-vtpm2), so no external swtpm binary is discovered here.

	if tc.SystemAARCH64 == "" || tc.Img == "" {
		if err := ensureDownloaded(cacheDir, tc); err != nil {
			return nil, err
		}
	}

	return tc, tc.validate()
}

// validate reports the first missing required artifact with an actionable hint.
func (tc *Toolchain) validate() error {
	switch {
	case tc.SystemAARCH64 == "":
		return missing("qemu-system-aarch64", "GUESTWEAVE_QEMU_SYSTEM_AARCH64")
	case tc.Img == "":
		return missing("qemu-img", "GUESTWEAVE_QEMU_IMG")
	case tc.FirmwareCode == "":
		return missing("the edk2 ARM UEFI firmware (edk2-aarch64-code.fd)", "GUESTWEAVE_QEMU_FIRMWARE_CODE")
	case tc.FirmwareVarsTemplate == "":
		return missing("the edk2 ARM UEFI vars template (edk2-arm-vars.fd)", "GUESTWEAVE_QEMU_FIRMWARE_VARS")
	}
	return nil
}

func missing(what, envVar string) error {
	return fmt.Errorf(
		"qemu: could not locate %s.\n"+
			"Install QEMU (e.g. `brew install qemu`) or set %s to its path.",
		what, envVar)
}

// accelerator chooses HVF on Apple Silicon, TCG otherwise. The downloaded/
// system binary must carry the com.apple.security.hypervisor entitlement for
// HVF to attach; EnsureHVFEntitlement signs cache-managed binaries.
func accelerator() string {
	if runtime.GOARCH == "arm64" {
		return "hvf"
	}
	return "tcg"
}

// firmwareCandidates expands names across each Homebrew share/qemu directory.
func firmwareCandidates(names []string) []string {
	var out []string
	for _, prefix := range homebrewPrefixes {
		for _, name := range names {
			out = append(out, filepath.Join(prefix, "share", "qemu", name))
		}
	}
	return out
}

// underHomebrew returns the first existing <prefix>/<parts...> path, or "".
func underHomebrew(parts ...string) string {
	for _, prefix := range homebrewPrefixes {
		p := filepath.Join(append([]string{prefix}, parts...)...)
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func lookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// firstExisting returns the first candidate that names an existing file.
func firstExisting(candidates ...string) string {
	for _, c := range candidates {
		if c != "" && fileExists(c) {
			return c
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ensureDownloaded is the auto-download hook for a pinned QEMU build placed
// under <cacheDir>/qemu. Auto-download requires a hosted, signed macOS QEMU
// artifact; until one is wired in, this surfaces an actionable error so a
// system install (Homebrew) or env override is used instead.
func ensureDownloaded(cacheDir string, tc *Toolchain) error {
	qemuDir := filepath.Join(cacheDir, "qemu")
	sys := filepath.Join(qemuDir, "bin", "qemu-system-aarch64")
	img := filepath.Join(qemuDir, "bin", "qemu-img")
	if fileExists(sys) && fileExists(img) {
		if tc.SystemAARCH64 == "" {
			tc.SystemAARCH64 = sys
		}
		if tc.Img == "" {
			tc.Img = img
		}
		// A cache-managed binary may need the HVF entitlement.
		if tc.Accel == "hvf" {
			_ = EnsureHVFEntitlement(tc.SystemAARCH64)
		}
		return nil
	}
	return fmt.Errorf(
		"qemu: no QEMU found and auto-download is not yet configured.\n"+
			"Install QEMU with `brew install qemu`, or place a build under %s, "+
			"or set GUESTWEAVE_QEMU_SYSTEM_AARCH64 / GUESTWEAVE_QEMU_IMG.", qemuDir)
}

// CreateDisk creates an empty qcow2 system disk of sizeGiB at path using
// qemu-img (imgTool). qcow2 is sparse, so the file starts small and grows.
func CreateDisk(imgTool, path string, sizeGiB uint64) error {
	cmd := exec.Command(imgTool, "create", "-f", diskFormat, path, fmt.Sprintf("%dG", sizeGiB))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("qemu: create disk %s: %v: %s", path, err, out)
	}
	return nil
}

// EnsureHVFEntitlement ad-hoc re-signs binPath with the
// com.apple.security.hypervisor entitlement if it lacks it, so `-accel hvf`
// works. It is a no-op when codesign already reports the entitlement. Only
// applied to cache-managed binaries (never to system/Homebrew installs).
func EnsureHVFEntitlement(binPath string) error {
	check := exec.Command("codesign", "-d", "--entitlements", "-", "--xml", binPath)
	if out, err := check.CombinedOutput(); err == nil &&
		bytes.Contains(out, []byte("com.apple.security.hypervisor")) {
		return nil
	}

	ent, err := os.CreateTemp("", "weave-qemu-hvf-*.plist")
	if err != nil {
		return err
	}
	defer os.Remove(ent.Name())
	const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>com.apple.security.hypervisor</key><true/>
</dict></plist>`
	if _, err := ent.WriteString(plist); err != nil {
		ent.Close()
		return err
	}
	ent.Close()

	sign := exec.Command("codesign", "--force", "--sign", "-",
		"--entitlements", ent.Name(), binPath)
	if out, err := sign.CombinedOutput(); err != nil {
		return fmt.Errorf("qemu: sign for HVF: %v: %s", err, out)
	}
	return nil
}
