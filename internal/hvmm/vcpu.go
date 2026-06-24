//go:build darwin

package hvmm

import (
	"fmt"
	"io"
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

	var stuckPC uint64
	stuckSamples := 0

	for {
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
			// The physical-timer firmware (FB21649319 workaround) does not arm the
			// virtual timer, so this should not normally fire. If it does, HVF has
			// already auto-masked it on exit; leave it masked and carry on. The
			// physical-timer PPI is delivered to the guest by the Apple vGIC with no
			// host involvement.

		default: // CANCELED — the watchdog forced the vCPU out so a momentarily
			// spinning guest can be sampled for liveness. No vtimer re-arm is needed
			// any more: with the physical-timer firmware the Apple vGIC delivers the
			// timer PPI at EL2 (the virtual-timer PPI is not delivered — FB21649319).
			pc := v.regOrZero(hv.HV_REG_PC)
			if pc == stuckPC {
				stuckSamples++
			} else {
				stuckPC, stuckSamples = pc, 0
			}
			if stuckSamples >= 5 {
				hint := ""
				if v.ReadMem != nil {
					if code := v.ReadMem(pc&^3, 16); code != nil {
						hint = fmt.Sprintf(" code=%x", code)
					}
				}
				return fmt.Errorf("guest hung near PC=0x%x across %d watchdog samples%s", pc, stuckSamples, hint)
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

// emulateSysReg services a trapped system-register access. For the EL2 registers
// HVF traps but the guest must really use, it proxies to the actual register;
// everything else stays permissive (reads return 0, writes are dropped) to keep
// firmware moving during bring-up.
func (v *VCPU) emulateSysReg(esr uint64) error {
	iss := esr & 0x1ffffff
	isRead := iss&1 == 1 // Direction bit (1 = read/MRS)
	rt := hv.Hv_reg_t((iss >> 5) & 0x1f)
	// Decode the encoded register identity (Op0/Op1/CRn/CRm/Op2) from the ISS.
	op0 := (iss >> 20) & 0x3
	op2 := (iss >> 17) & 0x7
	op1 := (iss >> 14) & 0x7
	crn := (iss >> 10) & 0xf
	crm := (iss >> 1) & 0xf

	// HVF traps CNTHCTL_EL2 (which the firmware programs to use the EL1 physical
	// timer — our FB21649319 workaround) and MDCCINT_EL1. Pass those through to the
	// real register via hv_vcpu_get/set_sys_reg, exactly as QEMU's HVF backend does
	// (see "target/arm: hvf: pass through CNTHCTL_EL2 and MDCCINT_EL1"); returning 0
	// would break the firmware's timer setup.
	if reg, ok := passthroughSysReg(op0, op1, crn, crm, op2); ok {
		if isRead {
			rc, val := hv.HvVcpuGetSysReg(v.id, reg)
			if err := hvErr("hv_vcpu_get_sys_reg", rc); err != nil {
				return err
			}
			if rt != 31 {
				if err := v.SetReg(hv.HV_REG_X0+rt, val); err != nil {
					return err
				}
			}
		} else {
			var val uint64
			if rt != 31 {
				val = v.regOrZero(hv.HV_REG_X0 + rt)
			}
			if err := hvErr("hv_vcpu_set_sys_reg", hv.HvVcpuSetSysReg(v.id, reg, val)); err != nil {
				return err
			}
		}
		return v.advancePC(esr)
	}

	v.tracef("MSR/MRS %s op0=%d op1=%d CRn=%d CRm=%d op2=%d (ISS=0x%x)",
		map[bool]string{true: "read", false: "write"}[isRead], op0, op1, crn, crm, op2, iss)
	if isRead && rt != 31 {
		if err := v.SetReg(hv.HV_REG_X0+rt, 0); err != nil {
			return err
		}
	}
	return v.advancePC(esr)
}

// passthroughSysReg maps the EL2 system registers HVF traps (and that the guest
// must really use) to their Hv_sys_reg_t, mirroring QEMU's HVF passthrough set.
func passthroughSysReg(op0, op1, crn, crm, op2 uint64) (hv.Hv_sys_reg_t, bool) {
	switch {
	case op0 == 3 && op1 == 4 && crn == 14 && crm == 1 && op2 == 0: // CNTHCTL_EL2
		return hv.HV_SYS_REG_CNTHCTL_EL2, true
	case op0 == 2 && op1 == 0 && crn == 0 && crm == 2 && op2 == 0: // MDCCINT_EL1
		return hv.HV_SYS_REG_MDCCINT_EL1, true
	}
	return 0, false
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
