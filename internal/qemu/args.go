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

	// InstallISO, when set, is attached as a bootable USB CD-ROM (first boot /
	// install). USB mass storage and xHCI have inbox Windows drivers, so WinPE
	// can boot and read the install source without out-of-box drivers.
	InstallISO string

	// VNCDisplay is the VNC display number; the server listens on
	// 127.0.0.1:(5900+VNCDisplay). VNCPasswordSet marks that password auth is
	// enabled (the secret is applied over QMP after launch).
	VNCDisplay     int
	VNCPasswordSet bool
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

	// Install media as a USB CD-ROM (inbox driver; bootable via UEFI).
	if s.InstallISO != "" {
		args = append(args,
			"-drive", fmt.Sprintf("if=none,id=cd0,media=cdrom,readonly=on,file=%s", s.InstallISO),
			"-device", fmt.Sprintf("usb-storage,drive=cd0,bootindex=%d", cdBoot),
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

	return args
}
