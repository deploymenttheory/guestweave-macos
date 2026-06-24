//go:build darwin

package hvmm

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unsafe"

	hv "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/hypervisor"
)

// AArch64 PSTATE for a vCPU entering at EL1h with all interrupts masked. The MMU
// is off at reset, so guest virtual addresses equal guest-physical addresses.
const cpsrEL1hMasked uint64 = 0x3c5 // M=0b0101 (EL1h) | DAIF (0x3c0)

// MMIOAccess describes a guest memory access that faulted to the host because no
// RAM is mapped there — i.e. an access to an (emulated) device register.
type MMIOAccess struct {
	Addr   uint64 // guest-physical address
	Write  bool   // true: guest store; false: guest load
	Bytes  int    // access width in bytes (1,2,4,8)
	Value  uint64 // store: data written by guest; load: handler sets the result
	VcpuID uint64 // the faulting vCPU (needed to proxy per-CPU GIC redistributor regs)
	reg    hv.Hv_reg_t
	isZR   bool
}

// Handler emulates the devices behind guest exits. Returning stop=true ends Run.
type Handler interface {
	// HandleMMIO services a guest device access (a stage-2 data abort).
	HandleMMIO(a *MMIOAccess) (stop bool, err error)
}

// VCPU is a virtual CPU. It is bound to the OS thread that created it; all of its
// methods must be called from that same thread (see package docs).
type VCPU struct {
	id    uint64
	exit  *hv.HvVcpuExitT
	exits int
	// Trace, if non-nil, receives one line per generically-handled trap
	// (system-register, HVC/SMC, WFI, vtimer) — a discovery aid for firmware.
	Trace io.Writer
	// MaxExits bounds the run by total VM-exit count (0 = unbounded), so a
	// fast-exit spin (e.g. a repeating vtimer) reports instead of hanging.
	MaxExits int
	// Watchdog, if > 0, force-exits the vCPU after that long without returning,
	// so a guest CPU loop (which never exits on its own) can be sampled rather
	// than spinning forever.
	Watchdog time.Duration
	// ReadMem, if set, reads guest memory — used to dump the instructions a stuck
	// guest is spinning on.
	ReadMem func(gpa uint64, n int) []byte
}

// SetReg writes a general/PC/PSTATE register (call on the vCPU's owning thread).
func (v *VCPU) SetReg(reg hv.Hv_reg_t, value uint64) error {
	return hvErr("hv_vcpu_set_reg", hv.HvVcpuSetReg(v.id, reg, value))
}

func (v *VCPU) sysReg(reg hv.Hv_sys_reg_t) uint64 {
	_, val := hv.HvVcpuGetSysReg(v.id, reg)
	return val
}

// StepTrace single-steps the guest (ARM software-step: MDSCR_EL1.SS + PSTATE.SS,
// with debug exceptions trapped to the host) and writes the guest's control-flow
// trail to Trace — one PC per non-sequential step (i.e. each taken branch/call),
// so the firmware's path is visible without millions of lines. It dispatches MMIO
// and system-register traps along the way, and stops when it detects a tight loop
// (a PC revisited stallThreshold times) or after maxSteps. It is a diagnostic for
// firmware bring-up; call on the vCPU's owning thread.
func (v *VCPU) StepTrace(h Handler, maxSteps int) error {
	const stallThreshold = 2000
	if err := hvErr("hv_vcpu_set_trap_debug_exceptions", hv.HvVcpuSetTrapDebugExceptions(v.id, true)); err != nil {
		return err
	}
	if err := hvErr("set MDSCR_EL1.SS", hv.HvVcpuSetSysReg(v.id, hv.HV_SYS_REG_MDSCR_EL1, v.sysReg(hv.HV_SYS_REG_MDSCR_EL1)|1)); err != nil {
		return err
	}
	var lastPC uint64
	seen := map[uint64]int{}
	for step := 0; step < maxSteps; step++ {
		// Re-arm the single-step state machine before each instruction.
		if err := v.SetReg(hv.HV_REG_CPSR, v.regOrZero(hv.HV_REG_CPSR)|(1<<21)); err != nil {
			return err
		}
		if err := hvErr("hv_vcpu_run", hv.HvVcpuRun(v.id)); err != nil {
			return err
		}
		if v.exit.Reason != hv.HV_EXIT_REASON_EXCEPTION {
			continue
		}
		esr := v.exit.Exception.Syndrome
		pc := v.regOrZero(hv.HV_REG_PC)
		switch ec := esr >> 26; ec {
		case 0x32, 0x33: // software step — one instruction retired
			if pc != lastPC+4 {
				fmt.Fprintf(v.Trace, "0x%06x\n", pc)
			}
			seen[pc]++
			if seen[pc] == stallThreshold {
				fmt.Fprintf(v.Trace, "[loop] PC 0x%x revisited %dx after %d steps — stopping\n", pc, stallThreshold, step)
				return nil
			}
			lastPC = pc
		case 0x24, 0x25: // MMIO data abort
			if _, err := v.handleDataAbort(esr, h); err != nil {
				return err
			}
			lastPC = 0
		case 0x18: // system-register trap
			if err := v.emulateSysReg(esr); err != nil {
				return err
			}
			lastPC = 0
		default:
			fmt.Fprintf(v.Trace, "[trap] EC=0x%02x at 0x%x while stepping\n", ec, pc)
			if err := v.advancePC(esr); err != nil {
				return err
			}
			lastPC = 0
		}
	}
	fmt.Fprintf(v.Trace, "[done] reached step budget %d\n", maxSteps)
	return nil
}

// NewVCPU creates a vCPU and sets its entry state (PC = entryPC, EL1h). It must
// be called on a thread that has called runtime.LockOSThread.
func (m *Machine) NewVCPU(entryPC uint64) (*VCPU, error) {
	rc, id, exit := hv.HvVcpuCreate(nil)
	if err := hvErr("hv_vcpu_create", rc); err != nil {
		return nil, err
	}
	// GICv3 uses affinity-based routing: per Apple's hv_gic docs the vCPU's
	// affinity MUST be set in MPIDR_EL1 before the in-kernel GIC realizes its
	// redistributor (otherwise hv_gic_get_redistributor_base fails and the
	// redistributor MMIO never maps). CPU 0 = affinity 0 with the RES1 bit, which
	// matches the DTB's cpu@0 reg=<0x0>.
	if err := hvErr("set MPIDR_EL1", hv.HvVcpuSetSysReg(id, hv.HV_SYS_REG_MPIDR_EL1, mpidrCPU0)); err != nil {
		return nil, err
	}
	// Establish the virtual counter: CNTVCT_EL0 = mach_absolute_time() - offset.
	// Parallels calls hv_vcpu_set_vtimer_offset at vCPU init; without it the guest
	// counter relationship is undefined and the vtimer never reaches its deadline.
	// Offset 0 makes the guest see the host monotonic counter directly.
	if err := hvErr("hv_vcpu_set_vtimer_offset", hv.HvVcpuSetVtimerOffset(id, 0)); err != nil {
		return nil, err
	}
	// Unmask the virtual timer so it fires (Parallels does this at vCPU init);
	// the run loop then turns a vtimer activation into an injected IRQ.
	if err := hvErr("hv_vcpu_set_vtimer_mask", hv.HvVcpuSetVtimerMask(id, false)); err != nil {
		return nil, err
	}
	if err := hvErr("set PC", hv.HvVcpuSetReg(id, hv.HV_REG_PC, entryPC)); err != nil {
		return nil, err
	}
	if err := hvErr("set CPSR", hv.HvVcpuSetReg(id, hv.HV_REG_CPSR, cpsrEL1hMasked)); err != nil {
		return nil, err
	}
	return &VCPU{id: id, exit: exit}, nil
}

// ID returns the Hypervisor.framework vCPU handle.
func (v *VCPU) ID() uint64 { return v.id }

// Destroy releases the vCPU (call on its owning thread).
func (v *VCPU) Destroy() error { return hvErr("hv_vcpu_destroy", hv.HvVcpuDestroy(v.id)) }

// Run steps the vCPU, dispatching exits to h, until h returns stop=true or an
// error occurs. It must be called on the vCPU's owning thread.
func (v *VCPU) Run(h Handler) error {
	if v.Watchdog > 0 {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(v.Watchdog)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					ids := []uint64{v.id} // force this vCPU out of hv_vcpu_run
					hv.HvVcpusExit(unsafe.Pointer(&ids[0]), 1)
				}
			}
		}()
	}

	pendingIRQ := false
	var stuckPC uint64
	stuckSamples := 0
	vtimerTicks := 0
	var lastTickPC uint64

	for {
		if pendingIRQ {
			if err := hvErr("hv_vcpu_set_pending_interrupt", hv.HvVcpuSetPendingInterrupt(v.id, hv.HV_INTERRUPT_TYPE_IRQ, true)); err != nil {
				return err
			}
		}
		if err := hvErr("hv_vcpu_run", hv.HvVcpuRun(v.id)); err != nil {
			return err
		}
		v.exits++
		if v.MaxExits > 0 && v.exits > v.MaxExits {
			return fmt.Errorf("exit budget %d exceeded (last reason=%s, PC=0x%x)", v.MaxExits, v.exit.Reason, v.regOrZero(hv.HV_REG_PC))
		}

		switch v.exit.Reason {
		case hv.HV_EXIT_REASON_EXCEPTION:
			esr := v.exit.Exception.Syndrome
			switch ec := esr >> 26; ec {
			case 0x24, 0x25: // Data Abort == guest MMIO.
				stop, err := v.handleDataAbort(esr, h)
				if err != nil || stop {
					return err
				}
			case 0x18: // Trapped MSR/MRS / system instruction.
				if err := v.emulateSysReg(esr); err != nil {
					return err
				}
			case 0x16, 0x17: // HVC / SMC.
				v.tracef("HVC/SMC EC=0x%02x — returning PSCI NOT_SUPPORTED", ec)
				if err := v.SetReg(hv.HV_REG_X0, ^uint64(0)); err != nil {
					return err
				}
				if err := v.advancePC(esr); err != nil {
					return err
				}
			case 0x01: // WFI/WFE.
				v.tracef("WFI/WFE at PC=0x%x", v.regOrZero(hv.HV_REG_PC))
				if err := v.advancePC(esr); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unhandled EC=0x%02x at PC=0x%x (ESR=0x%016x)", ec, v.regOrZero(hv.HV_REG_PC), esr)
			}

		case hv.HV_EXIT_REASON_VTIMER_ACTIVATED:
			// The guest's virtual timer fired (HVF auto-masks it on exit). Assert
			// the timer IRQ so a WFI-waiting guest wakes, then re-enable the timer.
			// (Validated against Parallels: VTIMER_ACTIVATED → raise PPI 27 → the
			// next run injects it via hv_vcpu_set_pending_interrupt.)
			v.tracef("VTIMER fired → asserting timer IRQ")
			pendingIRQ = true
			if err := hvErr("hv_vcpu_set_vtimer_mask", hv.HvVcpuSetVtimerMask(v.id, false)); err != nil {
				return err
			}

		default: // CANCELED — our watchdog forced the vCPU out (and may have
			// coalesced a vtimer activation). HVF masks the vtimer when it fires;
			// if it is masked, re-arm it so periodic timer interrupts keep flowing
			// to the guest via the in-kernel GIC, and treat that as progress.
			if _, masked := hv.HvVcpuGetVtimerMask(v.id); masked {
				_ = hv.HvVcpuSetVtimerMask(v.id, false)
				// NOTE: injecting HV_INTERRUPT_TYPE_IRQ here (as Parallels does for
				// its guest) delivers an IRQ edk2 takes at EL2 as an unhandled "IRQ
				// Exception" — the vtimer PPI isn't pending in the in-kernel GIC, so
				// ICC_IAR reads spurious. Routing the VTimer PPI to the guest GIC at
				// EL2 is the remaining piece; for now we only re-arm the vtimer.
				vtimerTicks++
				if vtimerTicks%50 == 0 {
					pc := v.regOrZero(hv.HV_REG_PC)
					moving := pc != lastTickPC
					hcr := v.sysReg(hv.HV_SYS_REG_HCR_EL2)
					v.tracef("vtimer tick %d: PC=0x%x moving=%v  HCR_EL2=0x%x(IMO=%d FMO=%d TGE=%d)  VBAR_EL2=0x%x VBAR_EL1=0x%x",
						vtimerTicks, pc, moving, hcr, (hcr>>4)&1, (hcr>>3)&1, (hcr>>27)&1,
						v.sysReg(hv.HV_SYS_REG_VBAR_EL2), v.sysReg(hv.HV_SYS_REG_VBAR_EL1))
					lastTickPC = pc
				}
				stuckPC, stuckSamples = 0, 0
				continue
			}
			pc := v.regOrZero(hv.HV_REG_PC)
			if pc == stuckPC {
				stuckSamples++
			} else {
				stuckPC, stuckSamples = pc, 0
			}
			ctl := v.sysReg(hv.HV_SYS_REG_CNTV_CTL_EL0)
			cval := v.sysReg(hv.HV_SYS_REG_CNTV_CVAL_EL0)
			voff := v.sysReg(hv.HV_SYS_REG_CNTVOFF_EL2)
			daif := (v.regOrZero(hv.HV_REG_CPSR) >> 6) & 0xf // PSTATE.{D,A,I,F}
			_, vtMasked := hv.HvVcpuGetVtimerMask(v.id)
			v.tracef("watchdog: PC=0x%x  CNTV_CTL=0x%x(EN=%d IMASK=%d ISTATUS=%d)  CVAL=0x%x  vtimerMasked=%v  DAIF=0x%x(I=%d)  exits=%d",
				pc, ctl, ctl&1, (ctl>>1)&1, (ctl>>2)&1, cval, vtMasked, daif, (daif>>1)&1, v.exits)
			_ = voff
			v.tracef("  regs: X19=0x%x X20=0x%x X21=0x%x X22=0x%x LR=0x%x",
				v.regOrZero(hv.HV_REG_X19), v.regOrZero(hv.HV_REG_X20), v.regOrZero(hv.HV_REG_X21), v.regOrZero(hv.HV_REG_X22), v.regOrZero(hv.HV_REG_LR))
			if stuckSamples == 1 && v.ReadMem != nil {
				lr := v.regOrZero(hv.HV_REG_LR)
				for _, blk := range []struct {
					name string
					at   uint64
				}{{"loop@LR", (lr &^ 3) - 0x20}, {"leaf@PC", pc &^ 3}} {
					if code := v.ReadMem(blk.at, 64); code != nil {
						var sb strings.Builder
						for i := 0; i+4 <= len(code); i += 4 {
							fmt.Fprintf(&sb, "%08x ", uint32(code[i])|uint32(code[i+1])<<8|uint32(code[i+2])<<16|uint32(code[i+3])<<24)
						}
						v.tracef("%s @0x%x: %s", blk.name, blk.at, sb.String())
					}
				}
			}
			if stuckSamples >= 3 {
				return fmt.Errorf("guest spinning near PC=0x%x: virtual timer enabled (CNTV_CTL=0x%x) but ISTATUS never trips — HVF vtimer not advancing in this EL2 VM", pc, ctl)
			}
		}
	}
}

func (v *VCPU) tracef(format string, a ...any) {
	if v.Trace != nil {
		fmt.Fprintf(v.Trace, "[trap] "+format+"\n", a...)
	}
}

func (v *VCPU) regOrZero(reg hv.Hv_reg_t) uint64 {
	_, val := hv.HvVcpuGetReg(v.id, reg)
	return val
}

// emulateSysReg services a trapped system-register access permissively: reads
// return 0 into the destination register, writes are dropped. Enough to keep
// firmware moving during bring-up; specific registers get real emulation later.
func (v *VCPU) emulateSysReg(esr uint64) error {
	iss := esr & 0x1ffffff
	isRead := iss&1 == 1 // Direction bit (1 = read)
	rt := hv.Hv_reg_t((iss >> 5) & 0x1f)
	v.tracef("MSR/MRS %s (ISS=0x%x)", map[bool]string{true: "read", false: "write"}[isRead], iss)
	if isRead && rt != 31 {
		if err := v.SetReg(hv.HV_REG_X0+rt, 0); err != nil {
			return err
		}
	}
	return v.advancePC(esr)
}

// advancePC moves PC past the trapped instruction (2 bytes for a 16-bit, 4 for a
// 32-bit instruction per ESR.IL).
func (v *VCPU) advancePC(esr uint64) error {
	pc := v.regOrZero(hv.HV_REG_PC)
	step := uint64(2)
	if (esr>>25)&1 == 1 {
		step = 4
	}
	return v.SetReg(hv.HV_REG_PC, pc+step)
}

// handleDataAbort decodes a stage-2 data abort into an MMIOAccess, calls the
// handler, applies a load result, and advances past the faulting instruction.
func (v *VCPU) handleDataAbort(esr uint64, h Handler) (stop bool, err error) {
	isv := (esr>>24)&1 == 1 // ISS valid (the syndrome describes the access)
	if !isv {
		return false, fmt.Errorf("data abort without valid ISS (ESR=0x%016x) — needs instruction decode", esr)
	}
	sas := (esr >> 22) & 0x3      // access size: 0=byte..3=doubleword
	srt := hv.Hv_reg_t((esr >> 16) & 0x1f) // transfer register
	a := MMIOAccess{
		Addr:   v.exit.Exception.Physical_address,
		Write:  (esr>>6)&1 == 1,
		Bytes:  1 << sas,
		VcpuID: v.id,
		reg:    hv.HV_REG_X0 + srt,
		isZR:   srt == 31,
	}
	if a.Write && !a.isZR {
		rc, val := hv.HvVcpuGetReg(v.id, a.reg)
		if err := hvErr("hv_vcpu_get_reg(srt)", rc); err != nil {
			return false, err
		}
		a.Value = val
	}
	stop, err = h.HandleMMIO(&a)
	if err != nil {
		return false, err
	}
	if !a.Write && !a.isZR {
		// Deliver the load result into the guest's destination register.
		if err := hvErr("hv_vcpu_set_reg(srt)", hv.HvVcpuSetReg(v.id, a.reg, a.Value)); err != nil {
			return false, err
		}
	}
	if stop {
		return true, nil
	}
	// Advance past the faulting instruction so the guest makes progress.
	rc, pc := hv.HvVcpuGetReg(v.id, hv.HV_REG_PC)
	if err := hvErr("hv_vcpu_get_reg(pc)", rc); err != nil {
		return false, err
	}
	step := uint64(2)
	if (esr>>25)&1 == 1 { // IL: 1 = 32-bit instruction
		step = 4
	}
	return false, hvErr("hv_vcpu_set_reg(pc)", hv.HvVcpuSetReg(v.id, hv.HV_REG_PC, pc+step))
}
