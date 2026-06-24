# HVF decode reference (from Binary Ninja's Hypervisor.framework type library)

Use these to read sections D (vCPU init) and E (run loop) of `libMonitorArm.md`, and
later to write the Go VMM. In Binary Ninja, apply the `hv_vcpu_exit_t` type to the exit
pointer and the `hv_reg_t` / `hv_sys_reg_t` enums to the register-id arguments — the
`set_reg`/`set_sys_reg`/exit-dispatch code then names itself.

## `hv_reg_t` (general registers — args to hv_vcpu_get/set_reg)
```
X0..X28 = 0x00..0x1c   FP = 0x1d   LR = 0x1e   PC = 0x1f
FPCR = 0x20   FPSR = 0x21   CPSR(PSTATE) = 0x22
```
→ The **initial PC** write in `vcpu_init` is `hv_vcpu_set_reg(v, 0x1f, <firmware entry GPA>)`.
  The initial PSTATE write is reg `0x22`.

## `hv_exit_reason_t` (the run-loop dispatch value, exit->reason)
```
CANCELED = 0   EXCEPTION = 1   VTIMER_ACTIVATED = 2   UNKNOWN = 3
```

## `hv_vcpu_exit_t` (the struct the run loop reads after hv_vcpu_run)
Canonical Apple layout (8-byte aligned; BN may show `exception` at +4 but it's +8):
```
+0x00  uint32  reason                      (hv_exit_reason_t)
+0x08  uint64  exception.syndrome          (ESR_EL2 value)
+0x10  uint64  exception.virtual_address   (FAR — faulting VA)
+0x18  uint64  exception.physical_address  (IPA — faulting guest-physical, for MMIO)
```
The `hv_vcpu_create(&vcpu, &exit, NULL)` 2nd arg is the pointer to this struct; the run
loop reads `exit.reason`, then for EXCEPTION decodes `exception.syndrome`.

## ESR_EL2 syndrome decode (exit->exception.syndrome)
`EC = syndrome >> 26` (exception class). The cases a VMM must handle:
```
EC 0x01  WFI/WFE trap            → idle the vcpu (and arm vtimer)
EC 0x16  HVC (from EL1/EL2)      → PSCI (CPU_ON/OFF, SYSTEM_OFF/RESET) + Windows hypercalls
EC 0x17  SMC                     → PSCI (when SMCCC via SMC)
EC 0x18  MSR/MRS/sys-insn trap   → emulate the trapped system register (ISS has reg + dir)
EC 0x20  Instruction Abort (lower EL)
EC 0x24  Data Abort (lower EL)   → MMIO: use exception.physical_address + ISS (size/SRT/WnR)
```
For a data abort (0x24), ISS bits give: `WnR` (bit 6: write=1), `SAS` (bits 22-23: access
size), `SRT` (bits 16-20: the GP register#). That + `physical_address` = the MMIO op to
emulate (route to GIC / UART / virtio device by address).

## `hv_memory_flags_t` (perms for hv_vm_map; the 3-entry table @0xa9d80)
```
HV_MEMORY_READ = 1   HV_MEMORY_WRITE = 2   HV_MEMORY_EXEC = 4
```
The map_region helper picks one of {R, RW, RWX} by a per-region type byte.

## Key `hv_sys_reg_t` IDs likely written in vcpu_init / handled in the MSR-trap path
```
MPIDR_EL1        = 0xffffc005   (per-core affinity; matches GIC redistributor)
SCTLR_EL1        = 0xffffc080
ID_AA64PFR0_EL1  = 0xffffc020   ID_AA64PFR1_EL1 = 0xffffc021
ID_AA64MMFR0..2  = 0xffffc038/39/3a
CPACR_EL1        = 0xffffc082   (FP/SIMD enable)
VBAR_EL1         = 0xffffc600
CNTKCTL_EL1      = 0xffffc708
CNTV_CTL_EL0     = 0xffffdf19   CNTV_CVAL_EL0 = 0xffffdf1a   (virtual timer — see hv_vcpu_set_vtimer_*)
```
With **EL2 enabled**, also expect EL2 sysreg / GIC ICH register handling
(`hv_gic_get/set_ich_reg`) in the interrupt path — that's the nested-virt machinery that
plain QEMU+HVF lacks.

## So the Go-VMM run loop skeleton (target shape)
```go
for {
    HvVcpuRun(vcpu)
    switch exit.reason {
    case EXCEPTION:
        ec := exit.exception.syndrome >> 26
        switch ec {
        case 0x24: emulateMMIO(exit.exception.physical_address, exit.exception.syndrome)
        case 0x18: emulateSysReg(exit.exception.syndrome)
        case 0x16, 0x17: psci(vcpu)            // CPU_ON brings up secondaries
        case 0x01: /* WFI */ waitForIRQ()
        }
        advancePC(vcpu, +4)   // for trapped instructions
    case VTIMER_ACTIVATED: injectVTimerIRQ()
    case CANCELED: /* async exit */
    }
}
```
