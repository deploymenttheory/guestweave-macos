// Service layer shared by the HTTP API server and the MCP server: the
// structured data behind the list/get commands, plus the detached-run spawn
// used because "run" must own a main thread and an AppKit run loop and must
// outlive the request that started it (the same reason lume detaches).
//go:build darwin

package vmservice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/deploymenttheory/guestweave/internal/macaddress"
	"github.com/deploymenttheory/guestweave/internal/vmconfig"
	"github.com/deploymenttheory/guestweave/internal/vmstorage"
)

// CollectVMInfos returns the structured listing for local and/or OCI VMs.
// source is "", "local" or "oci"; dates are RFC 3339.
func CollectVMInfos(source string) ([]weavecommand.ListVMInfo, error) {
	command := &weavecommand.ListCommand{Source: source, ISO8601Dates: true}

	var infos []weavecommand.ListVMInfo
	if source == "" || source == "local" {
		localStorage, err := vmstorage.NewVMStorageLocal()
		if err != nil {
			return nil, err
		}
		entries, err := localStorage.List()
		if err != nil {
			return nil, err
		}
		batch := make([]weavecommand.ListVMInfo, 0, len(entries))
		for _, entry := range entries {
			info, err := command.VMInfo("local", entry.Name, entry.VMDir)
			if err != nil {
				return nil, err
			}
			batch = append(batch, info)
		}
		infos = append(infos, weavecommand.SortedInfos(batch)...)
	}
	if source == "" || source == "oci" {
		ociStorage, err := vmstorage.NewVMStorageOCI()
		if err != nil {
			return nil, err
		}
		entries, err := ociStorage.List()
		if err != nil {
			return nil, err
		}
		batch := make([]weavecommand.ListVMInfo, 0, len(entries))
		for _, entry := range entries {
			info, err := command.VMInfo("OCI", entry.Name, entry.VMDir)
			if err != nil {
				return nil, err
			}
			batch = append(batch, info)
		}
		infos = append(infos, weavecommand.SortedInfos(batch)...)
	}
	return infos, nil
}

// VMDetails is the GET /weave/vms/{name} shape (lume's VMDetails).
type VMDetails struct {
	Name       string `json:"name"`
	OS         string `json:"os"`
	CPU        int    `json:"cpu"`
	MemoryMB   uint64 `json:"memoryMB"`
	DiskGB     int    `json:"diskGB"`
	DiskFormat string `json:"diskFormat"`
	Display    string `json:"display"`
	Running    bool   `json:"running"`
	State      string `json:"state"`
	IPAddress  string `json:"ipAddress,omitempty"`
}

// CollectVMDetails returns details for one local VM, including a
// best-effort IP lookup when the VM is running.
func CollectVMDetails(ctx context.Context, name string) (VMDetails, error) {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return VMDetails{}, err
	}
	vmDir, err := storage.Open(name)
	if err != nil {
		return VMDetails{}, err
	}
	vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return VMDetails{}, err
	}

	diskGB, err := vmDir.SizeGB()
	if err != nil {
		return VMDetails{}, err
	}
	running, err := vmDir.Running()
	if err != nil {
		return VMDetails{}, err
	}
	state, err := vmDir.State()
	if err != nil {
		return VMDetails{}, err
	}

	details := VMDetails{
		Name:       name,
		OS:         string(vmConfig.OS),
		CPU:        vmConfig.CPUCount,
		MemoryMB:   vmConfig.MemorySize / 1024 / 1024,
		DiskGB:     diskGB,
		DiskFormat: string(vmConfig.DiskFormat),
		Display:    vmConfig.Display.String(),
		Running:    running,
		State:      string(state),
	}

	if running {
		if mac, ok := macaddress.NewMACAddress(vmConfig.MACAddress.String()); ok {
			if ip, found, err := macaddress.ResolveIP(
				ctx,
				mac,
				macaddress.IPResolutionStrategyDHCP,
				0,
				vmDir.ControlSocketURL(),
			); err == nil &&
				found {
				details.IPAddress = ip.String()
			}
		}
	}
	return details, nil
}

// SpawnDetachedRun starts "weave run <name> <extraArgs...>" in its own
// session so it survives the calling request/server.
func SpawnDetachedRun(name string, extraArgs []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}

	args := append([]string{"run", name}, extraArgs...)
	command := exec.Command(executable, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Start(); err != nil {
		return err
	}
	// Reap the child when it eventually exits, avoiding zombies for the
	// lifetime of the server.
	go func() { _ = command.Wait() }()
	return nil
}

// WaitForVMRunning polls until the VM reports running or the timeout
// elapses; used by handlers that spawn a detached run.
func WaitForVMRunning(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		storage, err := vmstorage.NewVMStorageLocal()
		if err != nil {
			return err
		}
		vmDir, err := storage.Open(name)
		if err != nil {
			return err
		}
		if running, err := vmDir.Running(); err == nil && running {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("VM %q did not reach the running state within %s", name, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
