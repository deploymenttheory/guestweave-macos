// Snapshot (suspend/resume) support: bridges the macOS 14 save/restore
// machine-state APIs through manual blocks. Extracted from the run command
// when the monolith was split — methods on VM must live with the type.
//go:build darwin

package vm

import (
	"github.com/deploymenttheory/weave/internal/objcutil"

	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
	mainthread "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/custom/mainthread"
)

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
