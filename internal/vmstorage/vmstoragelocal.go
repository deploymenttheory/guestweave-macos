// Port of tart's VMStorageLocal.swift: the ~/.weave/vms local VM store.
//go:build darwin

package vmstorage

import (
	"os"
	"path/filepath"
	"time"

	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	"github.com/deploymenttheory/guestweave/internal/fsutil"
	"github.com/deploymenttheory/guestweave/internal/prune"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
)

// VMStorageLocal ports tart's VMStorageLocal class.
type VMStorageLocal struct {
	BaseURL string
}

var _ prune.PrunableStorage = (*VMStorageLocal)(nil)

// NewVMStorageLocal ports VMStorageLocal.init().
func NewVMStorageLocal() (*VMStorageLocal, error) {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}
	return &VMStorageLocal{
		BaseURL: filepath.Join(config.WeaveHomeDir, "vms"),
	}, nil
}

func (s *VMStorageLocal) vmURL(name string) string {
	return filepath.Join(s.BaseURL, name)
}

// Exists ports VMStorageLocal.exists(_:).
func (s *VMStorageLocal) Exists(name string) bool {
	return layout.NewVMDirectory(s.vmURL(name)).Initialized()
}

// Open ports VMStorageLocal.open(_:).
func (s *VMStorageLocal) Open(name string) (*layout.VMDirectory, error) {
	vmDir := layout.NewVMDirectory(s.vmURL(name))

	if err := vmDir.Validate(name); err != nil {
		return nil, err
	}

	if err := prune.UpdateAccessDate(vmDir.BaseURL, time.Now()); err != nil {
		return nil, err
	}

	return vmDir, nil
}

// Create ports VMStorageLocal.create(_:overwrite:).
func (s *VMStorageLocal) Create(name string, overwrite bool) (*layout.VMDirectory, error) {
	vmDir := layout.NewVMDirectory(s.vmURL(name))

	if err := vmDir.Initialize(overwrite); err != nil {
		return nil, err
	}

	return vmDir, nil
}

// Move ports VMStorageLocal.move(_:from:).
func (s *VMStorageLocal) Move(name string, from *layout.VMDirectory) error {
	if err := os.MkdirAll(s.BaseURL, 0o755); err != nil {
		return err
	}
	return FileManagerReplaceItem(s.vmURL(name), from.BaseURL)
}

// Rename ports VMStorageLocal.rename(_:_:).
func (s *VMStorageLocal) Rename(name string, newName string) error {
	return FileManagerReplaceItem(s.vmURL(newName), s.vmURL(name))
}

// Delete ports VMStorageLocal.delete(_:).
func (s *VMStorageLocal) Delete(name string) error {
	return layout.NewVMDirectory(s.vmURL(name)).Delete()
}

// LocalVMEntry is one element of VMStorageLocal.list()'s (name, VMDirectory)
// result tuple.
type LocalVMEntry struct {
	Name  string
	VMDir *layout.VMDirectory
}

// List ports VMStorageLocal.list().
func (s *VMStorageLocal) List() ([]LocalVMEntry, error) {
	entries, err := os.ReadDir(s.BaseURL)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var dirs []LocalVMEntry
	for _, entry := range entries {
		vmDir := layout.NewVMDirectory(filepath.Join(s.BaseURL, entry.Name()))
		if !vmDir.Initialized() {
			continue
		}
		dirs = append(dirs, LocalVMEntry{Name: vmDir.Name(), VMDir: vmDir})
	}
	return dirs, nil
}

// Prunables ports VMStorageLocal.prunables(): every VM not currently running.
func (s *VMStorageLocal) Prunables() ([]prune.Prunable, error) {
	dirs, err := s.List()
	if err != nil {
		return nil, err
	}

	var prunables []prune.Prunable
	for _, entry := range dirs {
		running, err := entry.VMDir.Running()
		if err != nil {
			return nil, err
		}
		if !running {
			prunables = append(prunables, entry.VMDir)
		}
	}
	return prunables, nil
}

// HasVMsWithMACAddress ports VMStorageLocal.hasVMsWithMACAddress(macAddress:).
func (s *VMStorageLocal) HasVMsWithMACAddress(macAddress string) (bool, error) {
	dirs, err := s.List()
	if err != nil {
		return false, err
	}
	for _, entry := range dirs {
		mac, err := entry.VMDir.MACAddress()
		if err != nil {
			return false, err
		}
		if mac == macAddress {
			return true, nil
		}
	}
	return false, nil
}

// FileManagerReplaceItem mirrors FileManager.replaceItemAt(_:withItemAt:):
// atomically replace originalItem with newItem via remove + rename, which is
// equivalent for tart's same-volume usage.
func FileManagerReplaceItem(originalItem string, newItem string) error {
	if fsutil.Exists(originalItem) {
		if err := os.RemoveAll(originalItem); err != nil {
			return err
		}
	}
	return os.Rename(newItem, originalItem)
}
