//go:build darwin

package hvmm

import (
	"encoding/binary"
	"fmt"
	"io"
	"runtime"
)

const (
	selfRAMBase  uint64 = 0x4000_0000
	selfRAMSize  int    = 0x20_0000   // 2 MiB
	selfUARTBase uint64 = 0x0900_0000 // intentionally unmapped → MMIO fault
)

// uartProbe is a minimal Handler: it captures the byte the guest writes to the
// fake UART and stops the run after that first device access.
type uartProbe struct{ got byte }

func (u *uartProbe) HandleMMIO(a *MMIOAccess) (bool, error) {
	u.got = byte(a.Value)
	return true, nil
}

// SelfTest proves the EL2 VMM path end to end inside weave: it creates an
// EL2-enabled VM, maps RAM holding four ARM64 instructions that store 'H' to a
// fake UART and then spin, runs a vCPU, and confirms the store trapped as MMIO.
// It is the in-weave equivalent of the discovery/govmm Milestone 0 prototype.
func SelfTest(out io.Writer) error {
	runtime.LockOSThread() // pin the vCPU to this OS thread for hv_vcpu_* calls
	defer runtime.UnlockOSThread()

	m, err := NewMachine()
	if err != nil {
		return err
	}
	defer m.Close()
	fmt.Fprintln(out, "✓ EL2-enabled VM created (the capability QEMU+HVF refuses)")

	ram, err := m.MapRAM(selfRAMBase, selfRAMSize)
	if err != nil {
		return err
	}
	// movz w0,#'H' ; movz x1,#0x09000000 ; strb w0,[x1] ; b .
	for i, ins := range []uint32{0x52800900, 0xd2a12001, 0x39000020, 0x14000000} {
		binary.LittleEndian.PutUint32(ram[i*4:], ins)
	}
	fmt.Fprintf(out, "✓ mapped %d KiB RAM at guest-physical 0x%08x\n", selfRAMSize/1024, selfRAMBase)

	vcpu, err := m.NewVCPU(selfRAMBase)
	if err != nil {
		return err
	}
	defer vcpu.Destroy()
	fmt.Fprintf(out, "✓ vCPU %d created, PC=0x%08x\n", vcpu.ID(), selfRAMBase)

	probe := &uartProbe{}
	fmt.Fprintln(out, "→ running guest…")
	if err := vcpu.Run(probe); err != nil {
		return err
	}
	if probe.got != 'H' {
		return fmt.Errorf("guest wrote 0x%02x, expected 'H'", probe.got)
	}
	fmt.Fprintf(out, "✓ MMIO trapped: guest wrote %q to the (unmapped) UART at 0x%08x\n", rune(probe.got), selfUARTBase)
	fmt.Fprintln(out, "\n🎉 hvmm self-test passed: weave ran an EL2 guest on Hypervisor.framework.")
	return nil
}
