# Discovery: Parallels' HVF VMM for Windows ARM64

## Objective

Reconstruct enough of how **Parallels Desktop** drives Apple's **Hypervisor.framework**
(`hv_*`) to boot **Windows 11 ARM64**, so we can build a minimal **Go VMM** (via
`go-bindings-macosplatform`'s `hypervisor` framework bindings) that does the same —
the one thing bare QEMU+HVF can't do on this Mac: **enable guest EL2**.

We are NOT cloning Parallels. We are recovering the *Hypervisor.framework usage
recipe* (a documented Apple API) for interoperability: the VM/GIC config values,
the guest memory map, the vCPU init state, and the VM-exit handling loop.

## Why this is the path

- Windows ARM64 requires **EL2** (ARM virtualization extensions). Apple HVF supports
  it on M3/M4 via `hv_vm_config_set_el2_enabled(config, true)`.
- QEMU 10.1.2's HVF backend never calls it → Windows hangs on the firmware logo.
- Parallels' `libMonitorArm.dylib` *does* call it (+ `hv_gic_create`). go-bindings
  already exposes `HvVmConfigSetEl2Enabled`, `HvGicCreate`, `HvVcpuCreate`, etc.

Host for this work: **Apple M4 / macOS 26**, HVF EL2 supported, IPA 40-bit.

## The API recipe (56 hv_* imports in libMonitorArm.dylib)

```
VM:    hv_vm_config_create, hv_vm_config_set_el2_enabled, hv_vm_config_set_ipa_size,
       hv_vm_config_get_{max,default}_ipa_size, hv_vm_create, hv_vm_map, hv_vm_unmap, hv_vm_destroy
GIC:   hv_gic_config_create, hv_gic_config_set_{distributor,redistributor,msi_region}_base,
       hv_gic_config_set_msi_interrupt_range, hv_gic_create, hv_gic_get/set_{icc,ich,icv,
       distributor,redistributor,msi}_reg, hv_gic_state_{create,get_data,get_size}, hv_gic_set_state,
       hv_gic_send_msi, hv_gic_set_spi, hv_gic_get_*_base_alignment, hv_gic_get_{intid,spi_interrupt_range}
vCPU:  hv_vcpu_create, hv_vcpu_destroy, hv_vcpu_run, hv_vcpu_get/set_reg, hv_vcpu_get/set_sys_reg,
       hv_vcpu_get/set_simd_fp_reg, hv_vcpu_set_pending_interrupt, hv_vcpu_set_vtimer_{mask,offset},
       hv_vcpus_exit, hv_vcpu_get_exec_time, hv_vcpu_{set_trap_debug_exceptions,get/set_trap_debug_reg_accesses}
```

## Reference

- **[hvf-reference.md](hvf-reference.md)** — distilled `hv_*` enums/struct layouts from
  Binary Ninja's Hypervisor.framework type library (`hv_reg_t` PC=0x1f, `hv_exit_reason_t`,
  `hv_vcpu_exit_t` layout, ESR_EL2 syndrome decode, key `hv_sys_reg_t` IDs). Use it to read
  the vCPU-init and run-loop disassembly and to shape the Go VMM.

## Method (per file)

In Binary Ninja, anchor on the **named imported `hv_*` symbols** (the binary is
stripped, but imports keep their names). Use **xrefs to each import** to find the
function that calls it, then read the surrounding code to recover constants
(addresses, sizes, IPA bits, register values). Record findings in that file's `.md`.

## File inventory & status

| # | File | Role | Status |
|---|------|------|--------|
| 1 | `MacOS/libMonitorArm.dylib` (1.0 MB, arm64) | **HVF VMM core** — VM/GIC/vCPU setup + exit loop | **in progress** → [libMonitorArm.md](libMonitorArm.md) |
| 2 | `Parallels VM.app/Contents/MacOS/*` | per-VM host process: device models, config, firmware load | not started |
| 3 | `Parallels VM.app/Contents/Resources/efia64.bin` (~964 KB) | Parallels' ARM64 UEFI firmware (Windows-tuned) | not started |
| 4 | `MacOS/libMonitorX86Emu.dylib` | x86-on-ARM emulator (out of scope for ARM Windows) | skip |

## What "done" looks like (the Go VMM MVP)

A Go program (using go-bindings `hypervisor`) that:
1. creates a VM config, `SetEl2Enabled(true)`, sets IPA size, `hv_vm_create`;
2. `hv_gic_create` with the recovered distributor/redistributor/MSI bases;
3. `hv_vm_map`s RAM + the UEFI firmware at the recovered guest-physical layout;
4. `hv_vcpu_create`s, sets the initial PC/sys-regs, and runs the exit loop
   (handle MMIO, sysreg traps, WFI, PSCI) far enough to see the firmware boot.

First milestone: firmware (edk2 or efia64.bin) prints to the (emulated) UART.
