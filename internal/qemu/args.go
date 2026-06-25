//go:build darwin

package qemu

import (
	"fmt"

	"github.com/deploymenttheory/weave/internal/vmconfig"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
)

const (
	// diskFormat is the on-disk format weave uses for QEMU system disks.
	diskFormat = "qcow2"
	// defaultMemoryMiB / defaultCPUs are used when the config leaves them unset.
	defaultMemoryMiB = 4096
	defaultCPUs      = 4
)

// Spec carries everything BuildArgs needs to assemble a qemu-system-aarch64
// command line for a Windows 11 ARM64 guest.
type Spec struct {
	Toolchain *Toolchain
	Config    *vmconfig.VMConfig
	VMDir     *vmdirectory.VMDirectory

	// InstallISO, when set, is attached as a second NVMe drive with bootindex 0
	// so UEFI boots \efi\boot\bootaa64.efi from it before the system disk.
	// NVMe is used instead of USB CD-ROM because emulated USB CD-ROM stalls
	// WinPE during loading; NVMe has an inbox ARM64 Windows driver.
	InstallISO string

	// VNCDisplay is the VNC display number; the server listens on
	// 127.0.0.1:(5900+VNCDisplay). VNCPasswordSet marks that password auth is
	// enabled (the secret is applied over QMP after launch).
	VNCDisplay     int
	VNCPasswordSet bool

	// TPMSocket, when non-empty, is the swtpm control socket; a TPM 2.0 device
	// is attached (Windows 11 requires it).
	TPMSocket string
}

// BuildArgs assembles the qemu-system-aarch64 argument list. Device choices
// target Windows 11 ARM64 inbox drivers: NVMe for the system disk, USB-storage
// for the install CD, ramfb for display (driven by UEFI GOP, captured by VNC),
// and USB keyboard + tablet for input.
func BuildArgs(s Spec) []string {
	cpus := s.Config.CPUCount
	if cpus <= 0 {
		cpus = defaultCPUs
	}
	memMiB := int(s.Config.MemorySize / (1024 * 1024))
	if memMiB <= 0 {
		memMiB = defaultMemoryMiB
	}

	cpuModel := "host"
	if s.Toolchain.Accel != "hvf" {
		cpuModel = "max" // TCG emulation cannot pass through the host CPU
	}

	args := []string{
		"-name", s.VMDir.Name(),
		"-machine", "virt,highmem=on",
		"-accel", s.Toolchain.Accel,
		"-cpu", cpuModel,
		"-smp", fmt.Sprintf("%d", cpus),
		"-m", fmt.Sprintf("%dM", memMiB),
		// Windows keeps the RTC in local time.
		"-rtc", "base=localtime",
	}

	// UEFI firmware: read-only code (unit 0) + writable per-VM vars (unit 1).
	args = append(args,
		"-drive", fmt.Sprintf("if=pflash,format=raw,unit=0,file=%s,readonly=on", s.Toolchain.FirmwareCode),
		"-drive", fmt.Sprintf("if=pflash,format=raw,unit=1,file=%s", s.VMDir.EFIVarsURL()),
	)

	// Boot order: prefer the install CD when present, else the system disk.
	diskBoot, cdBoot := 0, -1
	if s.InstallISO != "" {
		cdBoot, diskBoot = 0, 1
	}

	// System disk on NVMe (inbox Windows ARM64 driver).
	args = append(args,
		"-drive", fmt.Sprintf("if=none,id=disk0,file=%s,format=%s,cache=writeback,discard=unmap", s.VMDir.DiskURL(), diskFormat),
		"-device", fmt.Sprintf("nvme,drive=disk0,serial=weave0,bootindex=%d", diskBoot),
	)

	// USB controller + input devices. usb-tablet gives an absolute pointer,
	// which tracks the VNC cursor without grab.
	args = append(args,
		"-device", "qemu-xhci,id=usb",
		"-device", "usb-kbd",
		"-device", "usb-tablet",
	)

	// Install media on NVMe (inbox Windows ARM64 driver; far faster than an
	// emulated USB CD-ROM, which stalls WinPE loading). UEFI boots
	// \efi\boot\bootaa64.efi from it via the default removable-media path.
	if s.InstallISO != "" {
		args = append(args,
			"-drive", fmt.Sprintf("if=none,id=cd0,file=%s,format=raw,readonly=on", s.InstallISO),
			"-device", fmt.Sprintf("nvme,drive=cd0,serial=weavecd,bootindex=%d", cdBoot),
		)
	}

	// TPM 2.0 via the in-process, swtpm-compatible Go emulator (go-sdk-vtpm2),
	// which Windows 11 requires. QEMU's tpm-emulator backend connects to the
	// emulator's control socket; the ARM virt machine exposes it through the
	// MMIO tpm-tis-device.
	if s.TPMSocket != "" {
		args = append(args,
			"-chardev", fmt.Sprintf("socket,id=chrtpm,path=%s", s.TPMSocket),
			"-tpmdev", "emulator,id=tpm0,chardev=chrtpm",
			"-device", "tpm-tis-device,tpmdev=tpm0",
		)
	}

	// Display: ramfb is a firmware-driven framebuffer (UEFI GOP) that QEMU's
	// VNC server captures, so WinPE/Setup is visible without a guest GPU driver.
	args = append(args, "-device", "ramfb")

	vnc := fmt.Sprintf("127.0.0.1:%d", s.VNCDisplay)
	if s.VNCPasswordSet {
		vnc += ",password=on"
	}
	args = append(args, "-vnc", vnc)

	// QMP control socket for state queries and graceful power-down.
	args = append(args,
		"-qmp", fmt.Sprintf("unix:%s,server=on,wait=off", s.VMDir.QMPSocketURL()),
	)

	// Serial console (PL011) captured to a log — gives boot visibility when the
	// graphical framebuffer stops updating after the firmware hands off.
	args = append(args, "-serial", "file:"+s.VMDir.SerialLogURL())

	return args
}
