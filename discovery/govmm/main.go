//go:build darwin

// Command govmm is Milestone 0 of a from-scratch Go VMM on Apple's
// Hypervisor.framework: it proves we can do via Go (through the patched
// go-bindings idiomatic hypervisor layer) the one thing bare QEMU+HVF cannot on
// an M3/M4 Mac — create a VM with guest EL2 enabled — and then run a real guest
// vCPU far enough to trap an MMIO access.
//
// It creates an EL2 VM, maps a page of RAM holding four ARM64 instructions that
// store a byte to an (unmapped) UART address and then spin, creates a vCPU, runs
// it, and decodes the resulting stage-2 data-abort exit. Seeing the guest's
// 'H' arrive at the UART address proves the whole EL2-VMM-via-Go path end to end.
//
// Build + run (the binary must carry the hypervisor entitlement):
//
//	go build -o govmm . && \
//	codesign --force --sign - --entitlements entitlements.plist govmm && \
//	./govmm
package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"syscall"
	"unsafe"

	hv "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/hypervisor"
)

const (
	ramBase  uint64 = 0x4000_0000 // guest-physical base of RAM
	ramSize  int    = 0x20_0000   // 2 MiB
	uartBase uint64 = 0x0900_0000 // PL011-ish MMIO address, intentionally NOT mapped

	memRead  uint64 = 1
	memWrite uint64 = 2
	memExec  uint64 = 4

	cpsrEL1hMasked uint64 = 0x3c5 // M=EL1h (0x5) + DAIF masked (0x3c0)
)

func main() {
	// 1. Is guest EL2 available? (M3/M4 + recent macOS.)
	if rc, ok := hv.HvVmConfigGetEl2Supported(); rc != 0 || !ok {
		log.Fatalf("EL2 not supported here (rc=0x%x ok=%v) — needs Apple M3/M4 + macOS 15+", rc, ok)
	}
	fmt.Println("✓ EL2 (nested virtualization) supported on this host")

	// 2. Create a VM config with EL2 enabled and a 40-bit IPA, then create the VM.
	cfg := hv.HvVmConfigCreate()
	if cfg == nil {
		log.Fatal("hv_vm_config_create returned nil")
	}
	must("hv_vm_config_set_ipa_size", hv.HvVmConfigSetIpaSize(cfg, 40))
	must("hv_vm_config_set_el2_enabled", hv.HvVmConfigSetEl2Enabled(cfg, true))
	must("hv_vm_create", hv.HvVmCreate(cfg))
	fmt.Println("✓ EL2-enabled VM created — exactly what QEMU+HVF refuses to do")

	// 3. Host-backed RAM: anonymous mmap is page-aligned and never moved by the Go
	//    GC, so its address is stable for the lifetime of the mapping.
	mem, err := syscall.Mmap(-1, 0, ramSize,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		log.Fatalf("mmap host RAM: %v", err)
	}

	// Guest program (MMU off, so VA == PA):
	//   movz w0, #0x48          ; w0 = 'H'
	//   movz x1, #0x0900, lsl 16; x1 = 0x09000000 (UART)
	//   strb w0, [x1]           ; store -> stage-2 fault (UART unmapped) -> exit
	//   b .                     ; spin
	for i, ins := range []uint32{0x52800900, 0xd2a12001, 0x39000020, 0x14000000} {
		binary.LittleEndian.PutUint32(mem[i*4:], ins)
	}
	must("hv_vm_map", hv.HvVmMap(unsafe.Pointer(&mem[0]), ramBase, ramSize, memRead|memWrite|memExec))
	fmt.Printf("✓ mapped %d KiB RAM at guest-physical 0x%08x\n", ramSize/1024, ramBase)

	// 4. Create a vCPU (default config) and set its entry state.
	rc, vcpu, exit := hv.HvVcpuCreate(nil)
	must("hv_vcpu_create", rc)
	must("set PC", hv.HvVcpuSetReg(vcpu, hv.HV_REG_PC, ramBase))
	must("set CPSR", hv.HvVcpuSetReg(vcpu, hv.HV_REG_CPSR, cpsrEL1hMasked))
	fmt.Printf("✓ vCPU %d created, PC=0x%08x\n", vcpu, ramBase)

	// 5. Run loop: step the vCPU until it traps the MMIO store.
	fmt.Println("→ running guest…")
	for {
		must("hv_vcpu_run", hv.HvVcpuRun(vcpu))
		if exit.Reason != hv.HV_EXIT_REASON_EXCEPTION {
			log.Fatalf("unexpected exit reason %d (%s)", exit.Reason, exit.Reason)
		}
		esr := exit.Exception.Syndrome
		switch ec := esr >> 26; ec {
		case 0x24: // Data Abort from a lower EL == a guest MMIO access we must emulate.
			pa := exit.Exception.Physical_address
			srt := (esr >> 16) & 0x1f         // source/destination GP register
			isWrite := (esr>>6)&1 == 1        // WnR
			_, val := hv.HvVcpuGetReg(vcpu, hv.HV_REG_X0+hv.Hv_reg_t(srt))
			verb := "read"
			if isWrite {
				verb = "wrote"
			}
			fmt.Printf("✓ MMIO trapped: guest %s 0x%02x (%q) at 0x%08x via X%d\n",
				verb, val&0xff, rune(val&0xff), pa, srt)
			if pa == uartBase && isWrite && val&0xff == 'H' {
				fmt.Println("\n🎉 Milestone 0 reached: a Go VMM created an EL2 guest, executed")
				fmt.Println("   ARM64 instructions, and trapped + decoded a guest MMIO write.")
				os.Exit(0)
			}
			log.Fatalf("unexpected MMIO (pa=0x%x write=%v val=0x%x)", pa, isWrite, val)
		default:
			log.Fatalf("unhandled exception class EC=0x%02x (ESR=0x%016x)", ec, esr)
		}
	}
}

func must(what string, rc int) {
	if rc != 0 {
		log.Fatalf("%s failed: hv_return=0x%08x", what, uint32(rc))
	}
}
