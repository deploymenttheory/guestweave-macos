//go:build darwin

package hvmm

import (
	"fmt"
	"io"

	hv "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/hypervisor"
)

// GICv3 guest-physical layout — these MUST match the device tree's intc node.
// They are the QEMU "virt" GICv3 addresses that our embedded DTB declares.
const (
	gicDistBase      uint64 = 0x0800_0000 // distributor
	gicRedistBase    uint64 = 0x080A_0000 // redistributor region
	gicMsiBase       uint64 = 0x0808_0000 // MSI/ITS region
	gicMsiIntidBase  uint32 = 53          // MSI intid base (SPI range; Parallels-validated)
	gicMsiIntidCount uint32 = 64
)

// CreateGIC creates Apple's in-kernel GICv3 for the VM. Apple requires it after
// hv_vm_create and before any hv_vcpu_create, so the vCPUs are born with a GIC
// CPU interface. With it, the guest's GIC system registers (ICC_*) work natively
// rather than trapping, so firmware GIC drivers (edk2 ArmGicDxe) stop asserting
// on ICC_SRE_EL1. It logs Apple's required region alignments/sizes for reference.
func (m *Machine) CreateGIC(log io.Writer) error {
	if log != nil {
		_, distAlign := hv.HvGicGetDistributorBaseAlignment()
		_, distSize := hv.HvGicGetDistributorSize()
		_, redistAlign := hv.HvGicGetRedistributorBaseAlignment()
		_, redistRegion := hv.HvGicGetRedistributorRegionSize()
		_, msiAlign := hv.HvGicGetMsiRegionBaseAlignment()
		_, msiSize := hv.HvGicGetMsiRegionSize()
		fmt.Fprintf(log, "  GIC reqs: dist align=0x%x size=0x%x | redist align=0x%x region=0x%x | msi align=0x%x size=0x%x\n",
			distAlign, distSize, redistAlign, redistRegion, msiAlign, msiSize)
	}

	cfg := hv.HvGicConfigCreate()
	if cfg == nil {
		return fmt.Errorf("hv_gic_config_create returned nil")
	}
	if err := hvErr("hv_gic_config_set_distributor_base", hv.HvGicConfigSetDistributorBase(cfg, gicDistBase)); err != nil {
		return err
	}
	if err := hvErr("hv_gic_config_set_redistributor_base", hv.HvGicConfigSetRedistributorBase(cfg, gicRedistBase)); err != nil {
		return err
	}
	if err := hvErr("hv_gic_config_set_msi_region_base", hv.HvGicConfigSetMsiRegionBase(cfg, gicMsiBase)); err != nil {
		return err
	}
	if err := hvErr("hv_gic_config_set_msi_interrupt_range", hv.HvGicConfigSetMsiInterruptRange(cfg, gicMsiIntidBase, gicMsiIntidCount)); err != nil {
		return err
	}
	return hvErr("hv_gic_create", hv.HvGicCreate(cfg))
}
