//go:build darwin

package hvmm

import _ "embed"

// virtDTB is a flattened device tree describing a QEMU-"virt"-compatible machine
// (memory@40000000, pl011@9000000, intc@8000000 GICv3, arm,armv8-timer, cpu@0,
// /aliases). edk2 ArmVirtQemu's MemoryInit PEIM reads it from the base of system
// RAM (PcdDeviceTreeInitialBaseAddress = 0x40000000) to discover the memory map;
// without it that PEIM ASSERTs and CpuDeadLoops. Generated from QEMU
// (`-machine virt,gic-version=3,virtualization=on -m 256M -machine dumpdtb=`),
// trimmed to its real size so it sits below the firmware's SEC stack.
//
//go:embed virt.dtb
var virtDTB []byte
