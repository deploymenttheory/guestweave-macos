// suspendVM: the SIGUSR1 handler — validate save/restore support, pause,
// save machine state, and shut down.
//go:build darwin

package run

import (
	"context"
	"os"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
	"github.com/deploymenttheory/guestweave/internal/telemetry"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
)

func (c *Session) suspendVM(vmDir *layout.VMDirectory, cancelRun context.CancelFunc) {
	if !weaveplatform.MacOSAtLeast(14) {
		c.Reporter.Linef("%v",
			weaveerrors.ErrSuspendFailed(
				"this functionality is only supported on macOS 14 (Sonoma) or newer",
			),
		)
		telemetry.OTelShared().Flush()
		os.Exit(1)
	}

	validateErr := c.vm.ValidateSaveRestoreSupport()
	if validateErr != nil {
		// The running configuration can't be saved — typically the VM was
		// started without --suspendable, so it still carries USB input/entropy
		// devices, or its guest has no save/restore-compatible device set. The
		// VM has not been paused yet, so report the failure and leave it running
		// instead of tearing it down.
		c.Reporter.Linef("%v", weaveerrors.ErrSuspendFailed(validateErr.Error()))
		return
	}

	c.Reporter.Linef("pausing VM to take a snapshot...")
	if err := c.vm.SendErrorCompletion("pauseWithCompletionHandler:"); err != nil {
		c.Reporter.Linef("%v", weaveerrors.ErrSuspendFailed(err.Error()))
		telemetry.OTelShared().Flush()
		os.Exit(1)
	}
	c.Reporter.Linef("creating a snapshot...")
	if err := c.vm.SaveMachineStateTo(vmDir.StateURL()); err != nil {
		c.Reporter.Linef("%v", weaveerrors.ErrSuspendFailed(err.Error()))
		telemetry.OTelShared().Flush()
		os.Exit(1)
	}

	c.Reporter.Linef("snapshot created successfully! shutting down the VM...")
	cancelRun()
}
