//go:build darwin

// Package hvmm is weave's experimental from-scratch VMM built directly on Apple's
// Hypervisor.framework (the low-level hv_* API), via the idiomatic bindings in
// go-bindings-macosplatform. Unlike weave's QEMU backend, it can enable guest
// EL2 (hv_vm_config_set_el2_enabled) — the ARM virtualization extensions Windows
// 11 ARM64 requires and that QEMU's HVF accelerator refuses to provide.
//
// This is backend scaffolding: today it can create an EL2 VM, map guest memory,
// run a vCPU, and dispatch VM exits (MMIO, system-register traps, PSCI, WFI).
// Booting a full Windows guest additionally needs device models (GICv3, UART,
// virtio block/gpu), UEFI firmware, and a device tree — built out incrementally.
//
// Threading: Hypervisor.framework binds a vCPU to the OS thread that creates it;
// every hv_vcpu_* call for that vCPU must run on the same thread. Callers must
// therefore create and run a VCPU inside a single goroutine that has called
// runtime.LockOSThread (see SelfTest for the canonical pattern).
package hvmm

import (
	"fmt"
	"syscall"
	"unsafe"

	hv "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/hypervisor"
)

// Guest memory permissions for MapRAM (hv_memory_flags_t bits).
const (
	memRead  uint64 = 1
	memWrite uint64 = 2
	memExec  uint64 = 4
	memRWX          = memRead | memWrite | memExec
)

// hvErr turns a non-zero hv_return_t into an error.
func hvErr(what string, rc int) error {
	if rc == 0 {
		return nil
	}
	return fmt.Errorf("%s: hv_return=0x%08x", what, uint32(rc))
}

// Machine is one Hypervisor.framework VM: process-global config plus the host
// memory mapped into the guest's physical address space. Hypervisor.framework
// permits a single VM per process, so a process holds at most one Machine.
type Machine struct {
	regions []region
}

type region struct {
	gpa  uint64
	host []byte
}

// ReadGuest copies n bytes of guest memory at gpa, or nil if unmapped. Used for
// diagnostics and for device DMA (virtio).
func (m *Machine) ReadGuest(gpa uint64, n int) []byte {
	if h := m.guestSlice(gpa, n); h != nil {
		return append([]byte(nil), h...)
	}
	return nil
}

// WriteGuest copies data into guest memory at gpa, reporting whether the whole
// range was mapped. Used for device DMA (virtio writes results into guest RAM).
func (m *Machine) WriteGuest(gpa uint64, data []byte) bool {
	if h := m.guestSlice(gpa, len(data)); h != nil {
		copy(h, data)
		return true
	}
	return false
}

// guestSlice returns the host-backing slice for n bytes of guest memory at gpa,
// or nil if the range is not fully within one mapped region.
func (m *Machine) guestSlice(gpa uint64, n int) []byte {
	for _, r := range m.regions {
		if gpa >= r.gpa && gpa+uint64(n) <= r.gpa+uint64(len(r.host)) {
			off := gpa - r.gpa
			return r.host[off : off+uint64(n)]
		}
	}
	return nil
}

// NewMachine creates an EL2-enabled VM with a 40-bit intermediate-physical
// address space (matching Apple silicon / what Parallels uses for Windows). It
// fails if the host cannot provide guest EL2 (needs an M3/M4 and a recent macOS).
func NewMachine() (*Machine, error) {
	if rc, ok := hv.HvVmConfigGetEl2Supported(); rc != 0 || !ok {
		return nil, fmt.Errorf("guest EL2 not supported here (rc=0x%08x, ok=%v) — needs Apple M3/M4 + macOS 15+", uint32(rc), ok)
	}
	cfg := hv.HvVmConfigCreate()
	if cfg == nil {
		return nil, fmt.Errorf("hv_vm_config_create returned nil")
	}
	if err := hvErr("hv_vm_config_set_ipa_size", hv.HvVmConfigSetIpaSize(cfg, 40)); err != nil {
		return nil, err
	}
	if err := hvErr("hv_vm_config_set_el2_enabled", hv.HvVmConfigSetEl2Enabled(cfg, true)); err != nil {
		return nil, err
	}
	if err := hvErr("hv_vm_create", hv.HvVmCreate(cfg)); err != nil {
		return nil, err
	}
	return &Machine{}, nil
}

// MapROM maps size bytes of host-backed memory at gpa as read-only + executable
// for the guest (real flash semantics: the guest may execute but not write). The
// returned host slice is writable so the caller can load firmware into it.
func (m *Machine) MapROM(gpa uint64, size int) ([]byte, error) {
	host, err := syscall.Mmap(-1, 0, size,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("mmap %d bytes: %w", size, err)
	}
	if err := hvErr("hv_vm_map", hv.HvVmMap(unsafe.Pointer(&host[0]), gpa, size, memRead|memExec)); err != nil {
		_ = syscall.Munmap(host)
		return nil, err
	}
	m.regions = append(m.regions, region{gpa: gpa, host: host})
	return host, nil
}

// MapRAM allocates size bytes of host-backed, page-aligned RAM and maps it
// read/write/execute into the guest at guest-physical address gpa. It returns the
// host-side slice so the caller can load code/firmware into the guest.
func (m *Machine) MapRAM(gpa uint64, size int) ([]byte, error) {
	host, err := syscall.Mmap(-1, 0, size,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("mmap %d bytes: %w", size, err)
	}
	if err := hvErr("hv_vm_map", hv.HvVmMap(unsafe.Pointer(&host[0]), gpa, size, memRWX)); err != nil {
		_ = syscall.Munmap(host)
		return nil, err
	}
	m.regions = append(m.regions, region{gpa: gpa, host: host})
	return host, nil
}

// Close tears down the VM and releases all host memory.
func (m *Machine) Close() error {
	err := hvErr("hv_vm_destroy", hv.HvVmDestroy())
	for _, r := range m.regions {
		_ = syscall.Munmap(r.host)
	}
	m.regions = nil
	return err
}
