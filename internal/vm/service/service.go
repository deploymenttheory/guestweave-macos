// Service layer shared by the HTTP API server and the MCP server: the
// structured data behind the list/get commands, plus the detached-run spawn
// used because "run" must own a main thread and an AppKit run loop and must
// outlive the request that started it (the same reason lume detaches).
//go:build darwin

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"sort"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/macaddress"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
	vmstorage "github.com/deploymenttheory/guestweave/internal/vm/storage"
)

// VMInfo is one row of the VM listing: the shared DTO behind `weave list`,
// the HTTP API and the MCP server. The field names and order are load-bearing:
// they are the JSON keys, and the CLI renders them reflectively as table
// headers (skipping the field literally named "Running").
type VMInfo struct {
	Source   string
	Name     string
	Disk     int
	Size     int
	Accessed string
	Running  bool
	State    string
}

// ValidateListSource rejects anything but the known listing sources.
func ValidateListSource(source string) error {
	if source != "" && source != "local" && source != "oci" {
		return weaveerrors.ErrGeneric("'%s' is not a valid <source>", source)
	}
	return nil
}

// CollectVMInfos returns the structured listing for local and/or OCI VMs.
// source is "", "local" or "oci". iso8601 renders access dates as RFC 3339
// (JSON output, the HTTP API) instead of relative wording (text output).
func CollectVMInfos(source string, iso8601 bool) ([]VMInfo, error) {
	var infos []VMInfo
	if source == "" || source == "local" {
		localStorage, err := vmstorage.NewVMStorageLocal()
		if err != nil {
			return nil, err
		}
		entries, err := localStorage.List()
		if err != nil {
			return nil, err
		}
		batch := make([]VMInfo, 0, len(entries))
		for _, entry := range entries {
			info, err := collectVMInfo("local", entry.Name, entry.VMDir, iso8601)
			if err != nil {
				return nil, err
			}
			batch = append(batch, info)
		}
		infos = append(infos, sortedInfos(batch)...)
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
		batch := make([]VMInfo, 0, len(entries))
		for _, entry := range entries {
			info, err := collectVMInfo("OCI", entry.Name, entry.VMDir, iso8601)
			if err != nil {
				return nil, err
			}
			batch = append(batch, info)
		}
		infos = append(infos, sortedInfos(batch)...)
	}
	return infos, nil
}

func collectVMInfo(source, name string, vmDir *layout.VMDirectory, iso8601 bool) (VMInfo, error) {
	diskGB, err := vmDir.SizeGB()
	if err != nil {
		return VMInfo{}, err
	}
	sizeGB, err := vmDir.AllocatedSizeGB()
	if err != nil {
		return VMInfo{}, err
	}
	accessDate, err := vmDir.AccessDate()
	if err != nil {
		return VMInfo{}, err
	}
	running, err := vmDir.Running()
	if err != nil {
		return VMInfo{}, err
	}
	state, err := vmDir.State()
	if err != nil {
		return VMInfo{}, err
	}

	return VMInfo{
		Source:   source,
		Name:     name,
		Disk:     diskGB,
		Size:     sizeGB,
		Accessed: formatAccessDate(accessDate, iso8601),
		Running:  running,
		State:    string(state),
	}, nil
}

func sortedInfos(infos []VMInfo) []VMInfo {
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// formatAccessDate mirrors List.formatAccessDate: relative wording for text
// output, ISO 8601 for JSON.
func formatAccessDate(accessDate time.Time, iso8601 bool) string {
	if iso8601 {
		return accessDate.UTC().Format(time.RFC3339)
	}
	return relativeDateString(accessDate)
}

// relativeDateString approximates RelativeDateTimeFormatter's full style.
func relativeDateString(date time.Time) string {
	elapsed := time.Since(date)
	if elapsed < 0 {
		return "in the future"
	}

	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(elapsed.Hours()/24))
	}
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
	vmDir, err := vmstorage.OpenLocal(name)
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
		if ip, found, err := resolveIPWithConfig(
			ctx, vmConfig, vmDir.ControlSocketURL(),
			macaddress.IPResolutionStrategyDHCP, 0,
		); err == nil && found {
			details.IPAddress = ip
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
		vmDir, err := vmstorage.OpenLocal(name)
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
