//go:build darwin

package hvmm

import (
	"fmt"
	"io"
	"os"
	"runtime"
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
	bootRAMSize    int    = 0x1000_0000 // 256 MiB
	bootUARTBase   uint64 = 0x0900_0000
	cpsrEL2hMasked uint64 = 0x3c9 // M=EL2h (0x9) | DAIF (0x3c0) — firmware enters at EL2
)

// Platform is a minimal, deliberately permissive virt machine used to bring up
// firmware: it models a PL011 UART and, for any other device access, logs the
// address once and returns zero so the firmware keeps running and reveals what it
// touches (fw_cfg, GIC, RTC, …). It is a discovery aid, not a complete machine.
type Platform struct {
	uart     *pl011
	out      io.Writer
	exits    int
	maxExits int
	unknown  map[uint64]int
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
func Boot(out io.Writer, fwPath string, maxExits int) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	m, err := NewMachine()
	if err != nil {
		return err
	}
	defer m.Close()
	fmt.Fprintln(out, "✓ EL2-enabled VM created")

	fw, err := os.ReadFile(fwPath)
	if err != nil {
		return fmt.Errorf("read firmware %q: %w", fwPath, err)
	}
	flash, err := m.MapRAM(bootFlashBase, roundUp(len(fw), 0x1000))
	if err != nil {
		return err
	}
	copy(flash, fw)
	fmt.Fprintf(out, "✓ loaded firmware %s (%d MiB) at guest-physical 0x%08x\n", fwPath, len(fw)>>20, bootFlashBase)

	if _, err := m.MapRAM(bootVarsBase, bootVarsSize); err != nil {
		return err
	}
	if _, err := m.MapRAM(bootRAMBase, bootRAMSize); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ mapped %d MiB RAM at 0x%08x (+ %d MiB NV store at 0x%08x)\n",
		bootRAMSize>>20, bootRAMBase, bootVarsSize>>20, bootVarsBase)

	vcpu, err := m.NewVCPU(bootFlashBase)
	if err != nil {
		return err
	}
	defer vcpu.Destroy()
	vcpu.Trace = out
	vcpu.MaxExits = maxExits
	vcpu.Watchdog = 2 * time.Second // force a stuck guest out so we can sample its PC
	if err := vcpu.SetReg(hv.HV_REG_CPSR, cpsrEL2hMasked); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ vCPU %d entering firmware at PC=0x%08x (EL2h)\n\n--- firmware output ---\n", vcpu.ID(), bootFlashBase)

	p := &Platform{uart: &pl011{out: out}, out: out, maxExits: maxExits, unknown: map[uint64]int{}}
	runErr := vcpu.Run(p)
	fmt.Fprintf(out, "\n--- run ended after %d device exits ---\n", p.exits)
	return runErr
}

func roundUp(n, align int) int { return (n + align - 1) &^ (align - 1) }

func rwVerb(write bool) string {
	if write {
		return "write"
	}
	return "read"
}
