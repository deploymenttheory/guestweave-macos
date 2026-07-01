// Port of tart's VMStorageHelper.swift: the VMStorageHelper open/delete
// dispatchers, HasExitCode and the file-not-found NSError helpers. The former
// RuntimeError enum has been replaced by the lume-style domain error types in
// errors.go.
//go:build darwin

package vmstorage

import (
	"errors"
	"os"
	"time"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/oci"
	"github.com/deploymenttheory/guestweave/internal/vmdirectory"

	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/errkit"
)

// VMStorageHelperOpen ports VMStorageHelper.open(_:): dispatches to the OCI
// or local storage depending on whether name parses as a RemoteName.
func VMStorageHelperOpen(name string) (*vmdirectory.VMDirectory, error) {
	return missingVMWrap(name, func() (*vmdirectory.VMDirectory, error) {
		if remoteName, err := oci.NewRemoteName(name); err == nil {
			storage, err := NewVMStorageOCI()
			if err != nil {
				return nil, err
			}
			return storage.Open(remoteName, time.Now())
		}

		storage, err := NewVMStorageLocal()
		if err != nil {
			return nil, err
		}
		return storage.Open(name)
	})
}

// VMStorageHelperDelete ports VMStorageHelper.delete(_:).
func VMStorageHelperDelete(name string) error {
	_, err := missingVMWrap(name, func() (*vmdirectory.VMDirectory, error) {
		if remoteName, err := oci.NewRemoteName(name); err == nil {
			storage, err := NewVMStorageOCI()
			if err != nil {
				return nil, err
			}
			return nil, storage.Delete(remoteName)
		}

		storage, err := NewVMStorageLocal()
		if err != nil {
			return nil, err
		}
		return nil, storage.Delete(name)
	})
	return err
}

// missingVMWrap ports VMStorageHelper.missingVMWrap(_:closure:): PIDLock
// and file-not-found failures become VMDoesNotExist.
func missingVMWrap(name string, closure func() (*vmdirectory.VMDirectory, error)) (*vmdirectory.VMDirectory, error) {
	result, err := closure()
	if err == nil {
		return result, nil
	}

	var dirErr *weaveerrors.VMDirectoryError
	if errors.As(err, &dirErr) && dirErr.Kind == weaveerrors.VMDirectoryErrorPIDLockMissing {
		return nil, weaveerrors.ErrVMDoesNotExist(name)
	}
	if isFileNotFound(err) {
		return nil, weaveerrors.ErrVMDoesNotExist(name)
	}

	return nil, err
}

// isFileNotFound ports tart's NSError/Error isFileNotFound() extensions: true
// when err (or any of its underlying errors) is an NSError with a Cocoa
// file-not-found code.
// Cocoa file-not-found sentinels (NSFileNoSuchFileError / NSFileReadNoSuchFileError).
// errors.Is matches these on domain+code and walks the NSError underlying-error
// chain automatically via errkit.Error.Unwrap, so no manual recursion is needed.
var (
	errCocoaFileNoSuchFile     = errkit.New("NSCocoaErrorDomain", 4)
	errCocoaFileReadNoSuchFile = errkit.New("NSCocoaErrorDomain", 260)
)

func isFileNotFound(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, errCocoaFileNoSuchFile) ||
		errors.Is(err, errCocoaFileReadNoSuchFile)
}
