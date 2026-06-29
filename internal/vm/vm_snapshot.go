// Snapshot (suspend/resume) support: bridges the macOS 14 save/restore
// machine-state APIs through manual blocks. Extracted from the run command
// when the monolith was split — methods on VM must live with the type.
//go:build darwin

package vm

import (
	"fmt"

	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
	mainthread "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/custom/mainthread"
)

// Pause pauses the running VM's vCPUs, quiescing in-flight disk I/O. Resume
// restarts them.
func (vm *VM) Pause() error  { return vm.SendErrorCompletion("pauseWithCompletionHandler:") }
func (vm *VM) Resume() error { return vm.SendErrorCompletion("resumeWithCompletionHandler:") }

// CreateSnapshotPaused takes a full live snapshot (disk + firmware + RAM/device
// state) of a running VM: it pauses for a consistent capture, saves the machine
// state, clones the disk, then resumes. The VM keeps running afterwards, and
// reverting later resumes this exact moment rather than rebooting. (For a
// stopped VM, call vmDir.CreateSnapshot directly — there is no RAM to capture.)
func (vm *VM) CreateSnapshotPaused(vmDir *vmdirectory.VMDirectory, name, description string) (vmdirectory.Snapshot, error) {
	if err := vm.Pause(); err != nil {
		return vmdirectory.Snapshot{}, fmt.Errorf("failed to pause the VM: %w", err)
	}
	snap, createErr := vmDir.CreateSnapshot(vmdirectory.SnapshotCreateOptions{
		Name:               name,
		Description:        description,
		ExtraRequiredBytes: int64(vm.Config.MemorySize),
		SaveState:          vm.SaveMachineStateTo,
	})
	if resumeErr := vm.Resume(); resumeErr != nil && createErr == nil {
		createErr = fmt.Errorf("snapshot saved but failed to resume the VM: %w", resumeErr)
	}
	return snap, createErr
}

// restoreMachineStateFrom / saveMachineStateTo bridge the macOS 14 snapshot
// APIs through manual blocks.
func (vm *VM) RestoreMachineStateFrom(path string) error {
	return vm.sendURLErrorCompletion("restoreMachineStateFromURL:completionHandler:", path)
}

func (vm *VM) SaveMachineStateTo(path string) error {
	return vm.sendURLErrorCompletion("saveMachineStateToURL:completionHandler:", path)
}

func (vm *VM) sendURLErrorCompletion(selector string, path string) error {
	errCh := make(chan error, 1)
	block := purego.NewBlock(func(_ purego.Block, errID purego.ID) {
		if errID != 0 {
			errCh <- purego.NSErrorToError(errID)
		} else {
			errCh <- nil
		}
	})
	// Keep the bridged NSURL alive across the async send by capturing it in the
	// closure.
	url := objcutil.NSURLFromPath(path)
	mainthread.Do(func() {
		obj.ID(vm.VirtualMachine).Send(purego.RegisterName(selector), obj.ID(url), block)
	})
	return <-errCh
}
