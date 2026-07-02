// Port of tart's Commands/Get.swift. Rendering lives in the CLI layer; this
// command returns the VM's configuration as data.
//go:build darwin

package command

import (
	"context"
	"fmt"

	"github.com/deploymenttheory/guestweave/internal/vmconfig"

	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
	"github.com/deploymenttheory/guestweave/internal/vmstorage"
)

// GetVMInfo is the get command's result row.
type GetVMInfo struct {
	OS         weaveplatform.OS
	CPU        int
	Memory     uint64
	Disk       int
	DiskFormat string
	Size       string
	Display    string
	Running    bool
	State      string
}

// GetCommand ports the Get command.
type GetCommand struct {
	Name string
}

// Info collects the VM's configuration and on-disk state.
func (c *GetCommand) Info(ctx context.Context) (GetVMInfo, error) {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return GetVMInfo{}, err
	}
	vmDir, err := storage.Open(c.Name)
	if err != nil {
		return GetVMInfo{}, err
	}
	vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return GetVMInfo{}, err
	}

	diskGB, err := vmDir.SizeGB()
	if err != nil {
		return GetVMInfo{}, err
	}
	allocatedBytes, err := vmDir.AllocatedSizeBytes()
	if err != nil {
		return GetVMInfo{}, err
	}
	running, err := vmDir.Running()
	if err != nil {
		return GetVMInfo{}, err
	}
	state, err := vmDir.State()
	if err != nil {
		return GetVMInfo{}, err
	}

	return GetVMInfo{
		OS:         vmConfig.OS,
		CPU:        vmConfig.CPUCount,
		Memory:     vmConfig.MemorySize / 1024 / 1024,
		Disk:       diskGB,
		DiskFormat: string(vmConfig.DiskFormat),
		Size:       fmt.Sprintf("%.3f", float32(allocatedBytes)/1000/1000/1000),
		Display:    vmConfig.Display.String(),
		Running:    running,
		State:      string(state),
	}, nil
}
