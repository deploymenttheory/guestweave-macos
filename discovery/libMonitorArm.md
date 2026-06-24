# File 1: `libMonitorArm.dylib` — Parallels' HVF VMM core

- Path: `/Applications/Parallels Desktop.app/Contents/MacOS/libMonitorArm.dylib`
- Mach-O 64-bit dylib, **arm64**, 1,016,736 bytes, stripped (local symbols gone;
  imported `hv_*` / libc++ symbols retained → use those as anchors).
- Imports all 56 `hv_*` functions (see README). This is the only file that talks to
  Hypervisor.framework for ARM guests.

## How to work it in Binary Ninja

1. Open the dylib. Let analysis finish.
2. Symbols view → filter `hv_` to list the import stubs.
3. For each target import below, **right-click → Find references / Xrefs** to land on
   the caller. Read the caller (HLIL is easiest) and record the constants.
4. Rename functions as you identify them (e.g. `vm_setup`, `gic_setup`, `vcpu_init`,
   `run_loop`, `handle_exit`) so xrefs get readable.
5. Paste decompiled snippets + findings into the slots below.

> Tip: the most valuable constants are immediates near the `hv_*_set_*` calls
> (addresses, sizes, IPA bit-length, register IDs). Capture them verbatim.

---

## SEED FINDINGS (already recovered via objdump — jump here in Binary Ninja)

Entry function is exported: **`_MonOpen` @ `0x4e2c`** (the monitor-open routine; the
VM/GIC/vCPU setup all live inside or are called from it). Addresses below are vmaddr —
use Binary Ninja "Go to address". The `bl` targets resolve through `__stubs` (objdump
shows them as `dyld_stub_binder+0x...`; the stub→name map is in the table below).

### ✅ A. VM bootstrap — CONFIRMED (around `0x7a490`–`0x7a518`)

```
0x7a490  mov w8, #40                      ; IPA size = 40 bits
0x7a4a8  bl hv_vm_config_create           ; -> cfg in x20
0x7a4b8  bl hv_vm_config_set_ipa_size     ; (cfg, 40)
0x7a4e4  mov w1, #1
0x7a4e8  bl hv_vm_config_set_el2_enabled  ; (cfg, TRUE)   <-- THE KEY CALL
0x7a518  bl hv_vm_create                  ; (cfg)
```
EL2 is gated by a flag byte `ldrb w8,[x19,#0x5b]` (a per-VM "enable EL2" setting) — when
set, w1=1 is passed. **So: enable EL2, IPA=40, then create.** Exactly our recipe.

### ✅ B. GIC setup — CONFIRMED (around `0x7c280`–`0x7c378`), called AFTER vm_create

Guest-physical layout (immediates passed to the config setters):
```
distributor   base = 0x02010000   (hv_gic_config_set_distributor_base)
redistributor base = 0x02500000   (hv_gic_config_set_redistributor_base)
MSI region    base = 0x02250000   (hv_gic_config_set_msi_region_base)
MSI intid range    = base 53 (0x35), count 64 (0x40)   (hv_gic_config_set_msi_interrupt_range)
then  hv_gic_create
```
Each base is first validated against `hv_gic_get_*_base_alignment` (the `udiv/msub`
modulo checks at 0x7c290 etc.). Bases are stored in a global config struct at
`adrp 0xac000` + offsets 0x60/0x68/0x70/0x78/0x80/0xb8/0xc0.

### ⬜ C. Memory map — data-driven (needs Binary Ninja)

`hv_vm_map` has ONE call site (`0x7b164`) inside a generic helper **`map_region` @ `0x7b130`**:
```
0x7b154 ldp w8,w9,[x0]          ; region descriptor: w8=GPA>>12, w9=size>>12
0x7b158 lsl x1, x8, #12         ; x1 = GPA (bytes)
0x7b15c lsl x2, x9, #12         ; x2 = size (bytes)
0x7b150 ldr x3,[table+w8type]   ; x3 = HV_MEMORY flags from a 3-entry table @ 0xa9d80 (R/RW/RWX)
0x7b160 ldr x0,[x0,#0x28]       ; x0 = host uva
0x7b164 bl  hv_vm_map
```
**TODO in BN:** xref `map_region` (0x7b130) → find the **region-descriptor list/loop**
that drives it. That list IS the guest memory map (RAM base+size, firmware window for
`efia64.bin`, MMIO/framebuffer). Record each region's GPA/size/perms.

### ⬜ D. vCPU init — start at `hv_vcpu_create` call `0x7afcc`

`bl hv_vcpu_create @ 0x7afcc`. **TODO in BN:** in that function, find the
`hv_vcpu_set_reg(vcpu, HV_REG_PC, …)` (initial PC = firmware entry GPA), PSTATE/SP, and
any `hv_vcpu_set_sys_reg` (SCTLR, ID regs, CNTFRQ; EL2 sysregs since EL2 is on). The
`hv_vcpu_create` 2nd arg is the **exit struct pointer** — track it into the run loop.

### ⬜ E. Run loop — start at `hv_vcpu_run` call `0x7b380`

**TODO in BN:** the function holding `0x7b380` is the run loop. After `hv_vcpu_run`, find
the read of the exit struct's `reason` and the dispatch switch (ESR_EL2 syndrome →
data-abort/MMIO, MSR/MRS trap, HVC/SMC→PSCI, WFI). Note `hv_gic_set_*_reg` /
`hv_vcpu_set_pending_interrupt` uses (interrupt injection).

### Stub → name map (for reading `dyld_stub_binder+0x…` in objdump)
```
0x726c hv_vm_config_create        0x72a8 hv_vm_config_set_ipa_size
0x729c hv_vm_config_set_el2_enabled 0x72b4 hv_vm_create     0x72cc hv_vm_map
0x71a0 hv_vcpu_create  0x71f4 hv_vcpu_run
0x7050 gic_set_distributor_base  0x7074 gic_set_redistributor_base
0x7068 gic_set_msi_region_base   0x705c gic_set_msi_interrupt_range  0x7080 gic_create
0x708c gic_get_distributor_base_alignment 0x70ec gic_get_redistributor_base_alignment
0x70e0 gic_get_msi_region_base_alignment
```

---

## A. VM bootstrap  — anchor: `hv_vm_config_set_el2_enabled`, `hv_vm_create`

Goal: the exact create sequence + config values.

- [ ] Xref `hv_vm_config_create` → the VM-init function (call it `vm_setup`).
- [ ] In `vm_setup`, confirm `hv_vm_config_set_el2_enabled(cfg, ???)` — **is the 2nd
      arg `true`?** (the whole reason we're here).
- [ ] `hv_vm_config_set_ipa_size(cfg, N)` — record **N** (IPA bit-length; expect ~40).
- [ ] Order of calls before `hv_vm_create(cfg)`.

```
FINDINGS (paste HLIL of vm_setup):

el2_enabled arg =
ipa_size N      =
create order    =
```

## B. GIC setup  — anchor: `hv_gic_create`, `hv_gic_config_set_distributor_base`

Goal: the GIC's guest-physical layout (these are part of the memory map).

- [ ] Xref `hv_gic_config_create` → `gic_setup`.
- [ ] Record the immediates passed to:
  - `hv_gic_config_set_distributor_base(cfg, ADDR)` → **distributor base**
  - `hv_gic_config_set_redistributor_base(cfg, ADDR)` → **redistributor base**
  - `hv_gic_config_set_msi_region_base(cfg, ADDR)` → **MSI region base**
  - `hv_gic_config_set_msi_interrupt_range(cfg, BASE, COUNT)` → **MSI intid range**
- [ ] Is `hv_gic_create` called after `hv_vm_create`? (Apple requires VM first.)

```
FINDINGS:

distributor base    =
redistributor base  =
msi region base     =
msi intid range     =
```

## C. Guest memory map  — anchor: every `hv_vm_map`

Goal: where RAM, firmware, and device windows live in guest-physical space.

- [ ] List ALL xrefs to `hv_vm_map`. For each: `hv_vm_map(uva, GPA, SIZE, PERMS)`.
- [ ] Identify which one maps **RAM** (largest), which maps the **UEFI firmware**
      (`efia64.bin`, ~964 KB — look for a ~0x100000-ish size or a load of that file),
      and any device/framebuffer windows.
- [ ] Note PERMS (HV_MEMORY_READ/WRITE/EXEC) per mapping.

```
FINDINGS (table: GPA | size | perms | what):

```

## D. vCPU init  — anchor: `hv_vcpu_create`, `hv_vcpu_set_reg`, `hv_vcpu_set_sys_reg`

Goal: the initial CPU state handed to the firmware.

- [ ] Xref `hv_vcpu_create` → `vcpu_init`.
- [ ] Initial **PC** (`hv_vcpu_set_reg(vcpu, HV_REG_PC, X)`) — should be the firmware
      entry (GPA where `efia64.bin` is mapped).
- [ ] Initial **CPSR/PSTATE**, **SP**, **X0..X3** (DTB pointer? like x0=dtb on Linux —
      Windows/UEFI may differ).
- [ ] Any `hv_vcpu_set_sys_reg` for system registers (SCTLR, the ID regs, CNTFRQ, etc.).
      With EL2 enabled, watch for EL2 sysregs.

```
FINDINGS:

initial PC     =
PSTATE / SP    =
X0..X3         =
sys_reg writes =
```

## E. Run loop & exit handling  — anchor: `hv_vcpu_run`, `hv_vcpus_exit`

Goal: the dispatch we must replicate (the bulk of the VMM).

- [ ] Xref `hv_vcpu_run` → `run_loop`. After each run, it reads the **exit info**
      (the `hv_vcpu_exit_t` the vCPU's exit pointer was set to at create — find where
      that struct/pointer comes from; `hv_vcpu_create`'s 2nd arg is `exit`).
- [ ] Identify the exit-reason switch: which `HV_EXIT_REASON_*` cases exist
      (EXCEPTION, VTIMER_ACTIVATED, CANCELED), and how it decodes **ESR_EL2** /
      syndrome to dispatch:
  - data abort (MMIO) → device emulation,
  - trapped MSR/MRS (sysreg) → emulate the register,
  - HVC/SMC → **PSCI** (CPU_ON, etc.) and possibly Windows hypercalls,
  - WFI/WFE → idle.
- [ ] Note any use of `hv_gic_get/set_*_reg` inside the loop (interrupt delivery) and
      `hv_vcpu_set_pending_interrupt`.

```
FINDINGS (exit reasons handled + dispatch sketch):

```

---

## Synthesis (fill once A–E are done)

The minimal Go VMM bootstrap we will write against go-bindings `hypervisor`:

```
cfg := HvVmConfigCreate()
HvVmConfigSetEl2Enabled(cfg, true)
HvVmConfigSetIpaSize(cfg, <N from A>)
HvVmCreate(cfg)
gic := HvGicConfigCreate(); set <bases from B>; HvGicCreate(gic)
HvVmMap(<RAM>, <firmware>, ... from C)
vcpu := HvVcpuCreate(...); set PC/sysregs from D
for { HvVcpuRun(vcpu); switch exit.reason { ...from E... } }
```
