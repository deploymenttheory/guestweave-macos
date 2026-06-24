//go:build darwin

package hvmm

import (
	"encoding/gob"
	"fmt"
	"io"
	"unsafe"

	hv "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/hypervisor"
)

// snapshotGenRegs are the architectural general/PC/PSTATE registers captured per
// vCPU. X0..X30 plus PC, the FP control/status, and PSTATE.
var snapshotGenRegs = []hv.Hv_reg_t{
	hv.HV_REG_X0, hv.HV_REG_X1, hv.HV_REG_X2, hv.HV_REG_X3, hv.HV_REG_X4, hv.HV_REG_X5,
	hv.HV_REG_X6, hv.HV_REG_X7, hv.HV_REG_X8, hv.HV_REG_X9, hv.HV_REG_X10, hv.HV_REG_X11,
	hv.HV_REG_X12, hv.HV_REG_X13, hv.HV_REG_X14, hv.HV_REG_X15, hv.HV_REG_X16, hv.HV_REG_X17,
	hv.HV_REG_X18, hv.HV_REG_X19, hv.HV_REG_X20, hv.HV_REG_X21, hv.HV_REG_X22, hv.HV_REG_X23,
	hv.HV_REG_X24, hv.HV_REG_X25, hv.HV_REG_X26, hv.HV_REG_X27, hv.HV_REG_X28,
	hv.HV_REG_FP, hv.HV_REG_LR, hv.HV_REG_PC, hv.HV_REG_FPCR, hv.HV_REG_FPSR, hv.HV_REG_CPSR,
}

// snapshotSysRegs are the mutable system registers that define the vCPU's
// software state (MMU, exception, timer, thread-id, pointer-auth, debug). The
// read-only identity registers (MIDR/MPIDR/ID_AA64*) are set by the host and so
// are intentionally excluded — restoring them would fail and they never change.
var snapshotSysRegs = []hv.Hv_sys_reg_t{
	hv.HV_SYS_REG_SCTLR_EL1, hv.HV_SYS_REG_ACTLR_EL1, hv.HV_SYS_REG_CPACR_EL1,
	hv.HV_SYS_REG_TTBR0_EL1, hv.HV_SYS_REG_TTBR1_EL1, hv.HV_SYS_REG_TCR_EL1,
	hv.HV_SYS_REG_MAIR_EL1, hv.HV_SYS_REG_AMAIR_EL1, hv.HV_SYS_REG_VBAR_EL1,
	hv.HV_SYS_REG_SP_EL0, hv.HV_SYS_REG_SP_EL1, hv.HV_SYS_REG_ELR_EL1, hv.HV_SYS_REG_SPSR_EL1,
	hv.HV_SYS_REG_ESR_EL1, hv.HV_SYS_REG_FAR_EL1, hv.HV_SYS_REG_PAR_EL1,
	hv.HV_SYS_REG_AFSR0_EL1, hv.HV_SYS_REG_AFSR1_EL1,
	hv.HV_SYS_REG_CONTEXTIDR_EL1, hv.HV_SYS_REG_TPIDR_EL0, hv.HV_SYS_REG_TPIDR_EL1, hv.HV_SYS_REG_TPIDRRO_EL0,
	hv.HV_SYS_REG_CSSELR_EL1, hv.HV_SYS_REG_MDSCR_EL1, hv.HV_SYS_REG_MDCCINT_EL1,
	// EL2 state — the guest firmware/OS-loader runs at EL2, so its MMU, exception
	// vectors, and virtualization control all live in the EL2 register bank. These
	// must be captured for a faithful resume (without SCTLR_EL2/TTBR0_EL2/VBAR_EL2
	// the restored CPU faults immediately on its first instruction).
	hv.HV_SYS_REG_SCTLR_EL2, hv.HV_SYS_REG_TTBR0_EL2, hv.HV_SYS_REG_TTBR1_EL2,
	hv.HV_SYS_REG_TCR_EL2, hv.HV_SYS_REG_VTCR_EL2, hv.HV_SYS_REG_VTTBR_EL2,
	hv.HV_SYS_REG_MAIR_EL2, hv.HV_SYS_REG_VBAR_EL2, hv.HV_SYS_REG_HCR_EL2,
	hv.HV_SYS_REG_MDCR_EL2, hv.HV_SYS_REG_CPTR_EL2,
	hv.HV_SYS_REG_SP_EL2, hv.HV_SYS_REG_ELR_EL2, hv.HV_SYS_REG_SPSR_EL2,
	hv.HV_SYS_REG_ESR_EL2, hv.HV_SYS_REG_FAR_EL2, hv.HV_SYS_REG_HPFAR_EL2,
	hv.HV_SYS_REG_TPIDR_EL2, hv.HV_SYS_REG_VMPIDR_EL2, hv.HV_SYS_REG_VPIDR_EL2,
	hv.HV_SYS_REG_CNTHCTL_EL2, hv.HV_SYS_REG_CNTVOFF_EL2,
	hv.HV_SYS_REG_CNTHP_CTL_EL2, hv.HV_SYS_REG_CNTHP_CVAL_EL2,
	// timers
	hv.HV_SYS_REG_CNTKCTL_EL1, hv.HV_SYS_REG_CNTV_CTL_EL0, hv.HV_SYS_REG_CNTV_CVAL_EL0,
	hv.HV_SYS_REG_CNTP_CTL_EL0, hv.HV_SYS_REG_CNTP_CVAL_EL0,
	// pointer-authentication keys (Windows on ARM64 uses PAC)
	hv.HV_SYS_REG_APIAKEYLO_EL1, hv.HV_SYS_REG_APIAKEYHI_EL1,
	hv.HV_SYS_REG_APIBKEYLO_EL1, hv.HV_SYS_REG_APIBKEYHI_EL1,
	hv.HV_SYS_REG_APDAKEYLO_EL1, hv.HV_SYS_REG_APDAKEYHI_EL1,
	hv.HV_SYS_REG_APDBKEYLO_EL1, hv.HV_SYS_REG_APDBKEYHI_EL1,
	hv.HV_SYS_REG_APGAKEYLO_EL1, hv.HV_SYS_REG_APGAKEYHI_EL1,
}

// Snapshot is a serialisable point-in-time image of the VM: every vCPU's
// registers, the GIC state (Apple's opaque blob), the vtimer offset, and the
// contents of every guest memory region. It is enough to recreate the VM and
// resume it exactly where it left off — the basis of Windows VM save/restore.
type Snapshot struct {
	Version int
	VCPUs   []VCPUState
	GIC     []byte // hv_gic_state blob
	Regions []RegionState
}

// VCPUState is one vCPU's saved register file.
type VCPUState struct {
	Gen  map[uint32]uint64 // hv_reg_t -> value
	Sys  map[uint32]uint64 // hv_sys_reg_t -> value (only those that read back)
	Simd [][16]byte        // V0..V31 (128-bit each)
}

// RegionState is one guest-physical memory region's contents.
type RegionState struct {
	GPA  uint64
	Data []byte
}

const snapshotVersion = 1

// CaptureVCPU reads a stopped vCPU's full register file. The vCPU must not be
// running (call after Run returns or after forcing it out with hv_vcpus_exit).
func CaptureVCPU(v *VCPU) VCPUState {
	st := VCPUState{Gen: map[uint32]uint64{}, Sys: map[uint32]uint64{}}
	for _, r := range snapshotGenRegs {
		if rc, val := hv.HvVcpuGetReg(v.id, r); rc == 0 {
			st.Gen[uint32(r)] = val
		}
	}
	for _, r := range snapshotSysRegs {
		if rc, val := hv.HvVcpuGetSysReg(v.id, r); rc == 0 {
			st.Sys[uint32(r)] = val
		}
	}
	for i := 0; i < 32; i++ {
		var q [16]byte
		if hv.HvVcpuGetSimdFpReg(v.id, hv.Hv_simd_fp_reg_t(i), unsafe.Pointer(&q[0])) == 0 {
			st.Simd = append(st.Simd, q)
		}
	}
	return st
}

// RestoreVCPU writes a saved register file into a (freshly created) vCPU.
func RestoreVCPU(v *VCPU, st VCPUState) error {
	for i, q := range st.Simd {
		if err := hvErr("set simd", hv.HvVcpuSetSimdFpReg(v.id, hv.Hv_simd_fp_reg_t(i), unsafe.Pointer(&q[0]))); err != nil {
			return err
		}
	}
	for r, val := range st.Sys {
		// Tolerate registers the host treats as read-only on restore.
		_ = hv.HvVcpuSetSysReg(v.id, hv.Hv_sys_reg_t(r), val)
	}
	// General/PC/PSTATE last, so PC/CPSR are not clobbered by sysreg side effects.
	for r, val := range st.Gen {
		if err := hvErr("set reg", hv.HvVcpuSetReg(v.id, hv.Hv_reg_t(r), val)); err != nil {
			return err
		}
	}
	return nil
}

// CaptureGIC returns Apple's opaque GIC state blob (distributor, redistributors,
// CPU interfaces, MSI). The VM must be stopped.
func CaptureGIC() ([]byte, error) {
	state := hv.HvGicStateCreate()
	if state == nil {
		return nil, fmt.Errorf("hv_gic_state_create returned nil")
	}
	rc, size := hv.HvGicStateGetSize(state)
	if err := hvErr("hv_gic_state_get_size", rc); err != nil {
		return nil, err
	}
	blob := make([]byte, size)
	if err := hvErr("hv_gic_state_get_data", hv.HvGicStateGetData(state, unsafe.Pointer(&blob[0]))); err != nil {
		return nil, err
	}
	return blob, nil
}

// Snapshot captures the whole VM (every vCPU + GIC + all memory). vcpus must all
// be stopped.
func (m *Machine) Snapshot(vcpus ...*VCPU) (*Snapshot, error) {
	s := &Snapshot{Version: snapshotVersion}
	for _, v := range vcpus {
		s.VCPUs = append(s.VCPUs, CaptureVCPU(v))
	}
	gic, err := CaptureGIC()
	if err != nil {
		return nil, err
	}
	s.GIC = gic
	for _, r := range m.regions {
		data := make([]byte, len(r.host))
		copy(data, r.host)
		s.Regions = append(s.Regions, RegionState{GPA: r.gpa, Data: data})
	}
	return s, nil
}

// RestoreMachine recreates a VM from a snapshot: a fresh EL2 VM + GICv3, all
// memory regions reloaded, vCPUs created with their saved registers, then the
// saved GIC state applied. Hypervisor.framework permits one VM per process, so
// any prior Machine must be Closed first. Call on a runtime.LockOSThread'd
// goroutine (the vCPUs bind to the calling thread).
func RestoreMachine(s *Snapshot) (*Machine, []*VCPU, error) {
	m, err := NewMachine()
	if err != nil {
		return nil, nil, err
	}
	if err := m.CreateGIC(nil); err != nil {
		_ = m.Close()
		return nil, nil, fmt.Errorf("create GIC: %w", err)
	}
	for _, r := range s.Regions {
		host, err := m.MapRAM(r.GPA, len(r.Data))
		if err != nil {
			_ = m.Close()
			return nil, nil, err
		}
		copy(host, r.Data)
	}
	var vcpus []*VCPU
	for range s.VCPUs {
		v, err := m.NewVCPU(0) // PC/CPSR are overwritten by RestoreVCPU
		if err != nil {
			_ = m.Close()
			return nil, nil, err
		}
		vcpus = append(vcpus, v)
	}
	// The GIC state covers per-vCPU redistributors/CPU interfaces, so the vCPUs
	// must exist before it is applied.
	if len(s.GIC) > 0 {
		if err := hvErr("hv_gic_set_state", hv.HvGicSetState(unsafe.Pointer(&s.GIC[0]), len(s.GIC))); err != nil {
			_ = m.Close()
			return nil, nil, err
		}
	}
	for i, v := range vcpus {
		if err := RestoreVCPU(v, s.VCPUs[i]); err != nil {
			_ = m.Close()
			return nil, nil, err
		}
	}
	return m, vcpus, nil
}

// Encode serialises the snapshot (gob).
func (s *Snapshot) Encode(w io.Writer) error { return gob.NewEncoder(w).Encode(s) }

// ReadSnapshot deserialises a snapshot.
func ReadSnapshot(r io.Reader) (*Snapshot, error) {
	var s Snapshot
	if err := gob.NewDecoder(r).Decode(&s); err != nil {
		return nil, err
	}
	if s.Version != snapshotVersion {
		return nil, fmt.Errorf("unsupported snapshot version %d", s.Version)
	}
	return &s, nil
}
