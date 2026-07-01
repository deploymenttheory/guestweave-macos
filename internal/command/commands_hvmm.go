//go:build darwin

package command

import (
	"context"
	"fmt"
	"os"

	"github.com/deploymenttheory/guestweave/internal/hvmm"
)

// HvmmCommand drives weave's experimental Hypervisor.framework EL2 VMM backend
// (internal/hvmm). For now it exposes a single self-test that proves the backend
// can create an EL2 guest and dispatch a VM exit — the foundation for a future
// Windows-ARM64 backend that QEMU+HVF cannot provide.
type HvmmCommand struct {
	Action   string // "test" (default) or "boot"
	Firmware string // boot: path to an ARM64 UEFI firmware .fd (default: homebrew edk2)
	MaxExits int    // boot: bound the run by device-exit count (0 = unbounded)
	Step     bool   // boot: single-step trace the firmware's control flow
}

// Run executes the selected hvmm action.
func (c *HvmmCommand) Run(ctx context.Context) error {
	switch c.Action {
	case "", "test", "selftest":
		return hvmm.SelfTest(os.Stdout)
	case "boot":
		maxExits := c.MaxExits
		if maxExits == 0 && !c.Step {
			maxExits = 20000
		}
		return hvmm.Boot(os.Stdout, hvmm.ResolveFirmware(c.Firmware), maxExits, c.Step)
	case "snapshot":
		maxExits := c.MaxExits
		if maxExits == 0 {
			maxExits = 3000
		}
		return hvmm.SnapshotRoundTrip(os.Stdout, hvmm.ResolveFirmware(c.Firmware), "/tmp/weave-hvmm.snap", maxExits)
	default:
		return fmt.Errorf("usage: weave hvmm [test | boot [firmware.fd]] | snapshot [firmware.fd]]")
	}
}
