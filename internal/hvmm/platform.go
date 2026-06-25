//go:build darwin

package hvmm

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"time"

	hv "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/hypervisor"
)

// Guest-physical layout for a QEMU-"virt"-compatible platform (what edk2
// ArmVirtQemu firmware is built for): firmware flash at 0, RAM at 1 GiB, PL011
// UART at 0x09000000.
const (
	bootFlashBase  uint64 = 0x0000_0000
	bootVarsBase   uint64 = 0x0400_0000 // edk2 NV-variable store (pflash unit 1)
	bootVarsSize   int    = 0x0400_0000 // 64 MiB
	bootRAMBase    uint64 = 0x4000_0000
	bootRAMSize    int    = 0x1_0000_0000 // 4 GiB (matches virt.dtb memory@40000000; Windows 11 ARM64 minimum)
	bootUARTBase   uint64 = 0x0900_0000
	bootFwCfgBase  uint64 = 0x0902_0000
	bootEcamBase   uint64 = 0x40_1000_0000 // PCIe ECAM config space (DTB pcie node)
	bootEcamSize   uint64 = 0x1000_0000
	bootVirtioBase uint64 = 0x0a00_0000 // first virtio-mmio slot (DTB virtio nodes)
	bootVirtioSize uint64 = 0x200
	cpsrEL2hMasked uint64 = 0x3c9        // M=EL2h (0x9) | DAIF (0x3c0) — firmware enters at EL2
	mpidrCPU0      uint64 = 0x8000_0000 // MPIDR_EL1 for CPU 0: RES1 bit, affinity 0.0.0.0
)

// Platform is a minimal, deliberately permissive virt machine used to bring up
// firmware: it models a PL011 UART and, for any other device access, logs the
// address once and returns zero so the firmware keeps running and reveals what it
// touches (fw_cfg, GIC, RTC, …). It is a discovery aid, not a complete machine.
type Platform struct {
	uart      *pl011
	fwcfg     *fwcfg
	flash     *cfiFlash
	virtioblk *virtioBlk // nil unless a disk image is attached
	nvme      *nvmeController // nil unless an NVMe disk image is attached
	out       io.Writer
	exits     int
	maxExits  int
	unknown   map[uint64]int
}

// HandleMMIO routes a guest device access to the UART or logs it as unknown.
func (p *Platform) HandleMMIO(a *MMIOAccess) (bool, error) {
	p.exits++
	switch {
	case a.Addr >= bootUARTBase && a.Addr < bootUARTBase+0x1000:
		off := a.Addr - bootUARTBase
		if a.Write {
			p.uart.write(off, a.Bytes, a.Value)
		} else {
			a.Value = p.uart.read(off, a.Bytes)
		}
	case a.Addr >= bootVarsBase && a.Addr < bootVarsBase+uint64(bootVarsSize):
		if p.flash == nil {
			p.flash = newCFIFlash(bootVarsSize)
		}
		off := a.Addr - bootVarsBase
		if a.Write {
			p.flash.write(off, a.Bytes, a.Value)
		} else {
			a.Value = p.flash.read(off, a.Bytes)
		}
	case a.Addr >= bootFwCfgBase && a.Addr < bootFwCfgBase+0x18:
		if p.fwcfg == nil {
			p.fwcfg = newFwCfg(nil, nil) // fallback: legacy interface only, no ramfb
		}
		off := a.Addr - bootFwCfgBase
		if a.Write {
			p.fwcfg.write(off, a.Bytes, a.Value)
		} else {
			a.Value = p.fwcfg.read(off, a.Bytes)
		}
	case a.Addr >= gicRedistBase && a.Addr < gicRedistBase+0x20000:
		// Apple's in-kernel GIC maps the (shared) distributor itself but leaves the
		// per-vCPU redistributor to the VMM: we trap its MMIO and proxy each
		// register to Apple's GIC via hv_gic_get/set_redistributor_reg, whose reg
		// enum value is exactly the byte offset (validated against Parallels).
		reg := hv.Hv_gic_redistributor_reg_t(a.Addr - gicRedistBase)
		if a.Write {
			if rc := hv.HvGicSetRedistributorReg(a.VcpuID, reg, a.Value); rc != 0 && p.unknown[a.Addr] == 0 {
				fmt.Fprintf(p.out, "\n[gicr] set reg off=0x%x rc=0x%x (unproxied)\n", a.Addr-gicRedistBase, uint32(rc))
				p.unknown[a.Addr]++
			}
		} else {
			if rc, val := hv.HvGicGetRedistributorReg(a.VcpuID, reg); rc == 0 {
				a.Value = val
			} else {
				a.Value = 0
				if p.unknown[a.Addr] == 0 {
					fmt.Fprintf(p.out, "\n[gicr] get reg off=0x%x rc=0x%x (unproxied)\n", a.Addr-gicRedistBase, uint32(rc))
					p.unknown[a.Addr]++
				}
			}
		}
	case p.virtioblk != nil && a.Addr >= bootVirtioBase && a.Addr < bootVirtioBase+bootVirtioSize:
		off := a.Addr - bootVirtioBase
		if a.Write {
			p.virtioblk.write(off, a.Bytes, a.Value)
		} else {
			a.Value = p.virtioblk.read(off, a.Bytes)
		}
	case p.nvme != nil && p.nvme.inBAR(a.Addr):
		off := a.Addr - p.nvme.barBase()
		if a.Write {
			p.nvme.regWrite(off, a.Bytes, a.Value)
		} else {
			a.Value = p.nvme.regRead(off, a.Bytes)
		}
	case a.Addr >= bootEcamBase && a.Addr < bootEcamBase+bootEcamSize:
		// PCIe ECAM config space: bus0/dev0/fn0 is the NVMe controller (when
		// attached); every other function reads back all-ones so enumeration sees
		// it absent and stops, rather than finding a phantom device everywhere.
		off := a.Addr - bootEcamBase
		switch {
		case p.nvme != nil && off < 0x1000:
			if a.Write {
				p.nvme.configWrite(off, a.Bytes, a.Value)
			} else {
				a.Value = p.nvme.configRead(off, a.Bytes)
			}
		case !a.Write:
			a.Value = ^uint64(0) >> (64 - 8*uint(a.Bytes))
		}
	default:
		if p.unknown[a.Addr] == 0 {
			fmt.Fprintf(p.out, "\n[mmio] unhandled %s @ 0x%08x (%d-byte)\n", rwVerb(a.Write), a.Addr, a.Bytes)
		}
		p.unknown[a.Addr]++
		if !a.Write {
			a.Value = 0
		}
	}
	if p.maxExits > 0 && p.exits >= p.maxExits {
		fmt.Fprintf(p.out, "\n[stop] reached the %d-exit budget — halting discovery run\n", p.maxExits)
		return true, nil
	}
	return false, nil
}

// Boot loads a flat ARM64 UEFI firmware image (e.g. edk2-aarch64-code.fd) at
// guest-physical 0, maps RAM and an NV-variable store, and runs a vCPU entering
// the firmware at EL2. Device accesses are dispatched to a discovery Platform.
// maxExits bounds the run (0 = unbounded). It is the next step beyond SelfTest
// toward a real boot; it does not yet provide fw_cfg/GIC/DTB, so firmware will
// not complete — the trace shows exactly which devices to model next.
func Boot(out io.Writer, fwPath string, maxExits int, step bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Software-step (MDSCR_EL1.SS) only steps EL0/EL1, so the step-trace enters at
	// EL1h; the spin is identical at EL1, so the path it reveals is the same.
	entryCPSR, mode := cpsrEL2hMasked, "EL2h"
	if step {
		entryCPSR, mode = cpsrEL1hMasked, "EL1h, single-step trace"
	} else if os.Getenv("WEAVE_ENTRY_EL1") != "" {
		entryCPSR, mode = cpsrEL1hMasked, "EL1h" // diagnostic: virtual-timer IRQ targets EL1
	}
	m, vcpu, err := setupGuest(out, fwPath, entryCPSR)
	if err != nil {
		return err
	}
	defer m.Close()
	defer vcpu.Destroy()
	vcpu.Trace = out
	vcpu.MaxExits = maxExits
	if !step {
		// Liveness only: force a spinning guest out periodically so it can be
		// sampled. The physical-timer firmware's interrupts are delivered by the
		// Apple vGIC without host involvement, so this no longer paces the timer.
		vcpu.Watchdog = 2 * time.Second
		if s := os.Getenv("WEAVE_WATCHDOG_MS"); s != "" {
			if ms, err := strconv.Atoi(s); err == nil {
				vcpu.Watchdog = time.Duration(ms) * time.Millisecond
			}
		}
	}
	fmt.Fprintf(out, "✓ vCPU %d entering firmware at PC=0x%08x (%s)\n\n--- firmware output ---\n", vcpu.ID(), bootFlashBase, mode)

	uart := &pl011{out: out, in: make(chan byte, 256)}
	go uart.pumpInput(os.Stdin) // host stdin drives the guest serial console
	p := &Platform{uart: uart, out: out, maxExits: maxExits, unknown: map[uint64]int{}}
	// fw_cfg with DMA (needed for ramfb's config write) + a ramfb framebuffer.
	p.fwcfg = newFwCfg(m, newRamfb(m, out))
	if disk := os.Getenv("WEAVE_HVMM_DISK"); disk != "" {
		data, derr := os.ReadFile(disk)
		if derr != nil {
			return fmt.Errorf("read disk %q: %w", disk, derr)
		}
		p.virtioblk = newVirtioBlk(m, data)
		fmt.Fprintf(out, "[disk] virtio-blk %s (%d MiB) at 0x%08x\n", disk, len(data)>>20, bootVirtioBase)
	}
	if disk := os.Getenv("WEAVE_HVMM_NVME"); disk != "" {
		data, derr := os.ReadFile(disk)
		if derr != nil {
			return fmt.Errorf("read nvme disk %q: %w", disk, derr)
		}
		p.nvme = newNvmeController(m, data)
		fmt.Fprintf(out, "[disk] nvme %s (%d MiB) on PCIe bus0/dev0\n", disk, len(data)>>20)
	}
	var runErr error
	if step {
		budget := maxExits
		if budget == 0 {
			budget = 500000
		}
		runErr = vcpu.StepTrace(p, budget)
	} else {
		runErr = vcpu.Run(p)
	}
	fmt.Fprintf(out, "\n--- run ended after %d device exits ---\n", p.exits)
	return runErr
}

// setupGuest builds the standard guest used for both boot and snapshot: an EL2
// VM + in-kernel GICv3, the firmware mapped at guest-physical 0, RAM with the DTB
// at its base, an NV-variable store, and a vCPU entering at entryCPSR. On error
// it cleans up the partially-built VM. Caller owns Close/Destroy on success.
func setupGuest(out io.Writer, fwPath string, entryCPSR uint64) (*Machine, *VCPU, error) {
	m, err := NewMachine()
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintln(out, "✓ EL2-enabled VM created")
	if err := m.CreateGIC(out); err != nil {
		_ = m.Close()
		return nil, nil, fmt.Errorf("create GIC: %w", err)
	}
	fmt.Fprintln(out, "✓ in-kernel GICv3 created (CPU interface system registers active)")

	fw, err := os.ReadFile(fwPath)
	if err != nil {
		_ = m.Close()
		return nil, nil, fmt.Errorf("read firmware %q: %w", fwPath, err)
	}
	// Map the whole code-flash region (up to the NV store) and copy the firmware
	// into its base, so the image size is irrelevant — a compact CI-built FV and a
	// 64 MiB distro image both work.
	flash, err := m.MapRAM(bootFlashBase, int(bootVarsBase))
	if err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	if len(fw) > len(flash) {
		_ = m.Close()
		return nil, nil, fmt.Errorf("firmware %q (%d bytes) exceeds the %d-byte code-flash region", fwPath, len(fw), len(flash))
	}
	copy(flash, fw)
	// The NV variable store (pflash) is intentionally left unmapped so its accesses
	// trap and the CFI flash device (Platform.flash) can model the command/status
	// protocol edk2's NorFlashDxe needs.
	ram, err := m.MapRAM(bootRAMBase, bootRAMSize)
	if err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	copy(ram, virtDTB) // edk2 reads the device tree from RAM base
	fmt.Fprintf(out, "✓ firmware %s (%d MiB) + %d MiB RAM + %d-byte DTB + %d MiB NV store mapped\n",
		fwPath, len(fw)>>20, bootRAMSize>>20, len(virtDTB), bootVarsSize>>20)

	vcpu, err := m.NewVCPU(bootFlashBase)
	if err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	if err := vcpu.SetReg(hv.HV_REG_CPSR, entryCPSR); err != nil {
		_ = vcpu.Destroy()
		_ = m.Close()
		return nil, nil, err
	}
	vcpu.ReadMem = m.ReadGuest
	return m, vcpu, nil
}

func rwVerb(write bool) string {
	if write {
		return "write"
	}
	return "read"
}
