// Port of tart's Commands/List.swift. Rendering lives in the CLI layer; this
// command returns the sorted VM inventory as data.
//go:build darwin

package command

import (
	"context"
	"fmt"
	"sort"
	"time"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
	"github.com/deploymenttheory/guestweave/internal/vmstorage"
)

type ListVMInfo struct {
	Source   string
	Name     string
	Disk     int
	Size     int
	Accessed string
	Running  bool
	State    string
}

// ListCommand ports the List command.
type ListCommand struct {
	Source string // "", "local" or "oci"
	// ISO8601Dates renders access dates as RFC 3339 instead of relative
	// wording (used for JSON output and the HTTP API).
	ISO8601Dates bool
}

func (c *ListCommand) Validate() error {
	if c.Source != "" && c.Source != "local" && c.Source != "oci" {
		return weaveerrors.ErrGeneric("'%s' is not a valid <source>", c.Source)
	}
	return nil
}

// Infos collects the VM inventory from the selected sources.
func (c *ListCommand) Infos(ctx context.Context) ([]ListVMInfo, error) {
	var infos []ListVMInfo

	if c.Source == "" || c.Source == "local" {
		localStorage, err := vmstorage.NewVMStorageLocal()
		if err != nil {
			return nil, err
		}
		entries, err := localStorage.List()
		if err != nil {
			return nil, err
		}
		batch := make([]ListVMInfo, 0, len(entries))
		for _, entry := range entries {
			info, err := c.VMInfo("local", entry.Name, entry.VMDir)
			if err != nil {
				return nil, err
			}
			batch = append(batch, info)
		}
		infos = append(infos, SortedInfos(batch)...)
	}

	if c.Source == "" || c.Source == "oci" {
		ociStorage, err := vmstorage.NewVMStorageOCI()
		if err != nil {
			return nil, err
		}
		entries, err := ociStorage.List()
		if err != nil {
			return nil, err
		}
		batch := make([]ListVMInfo, 0, len(entries))
		for _, entry := range entries {
			info, err := c.VMInfo("OCI", entry.Name, entry.VMDir)
			if err != nil {
				return nil, err
			}
			batch = append(batch, info)
		}
		infos = append(infos, SortedInfos(batch)...)
	}

	return infos, nil
}

func (c *ListCommand) VMInfo(source string, name string, vmDir *layout.VMDirectory) (ListVMInfo, error) {
	diskGB, err := vmDir.SizeGB()
	if err != nil {
		return ListVMInfo{}, err
	}
	sizeGB, err := vmDir.AllocatedSizeGB()
	if err != nil {
		return ListVMInfo{}, err
	}
	accessDate, err := vmDir.AccessDate()
	if err != nil {
		return ListVMInfo{}, err
	}
	running, err := vmDir.Running()
	if err != nil {
		return ListVMInfo{}, err
	}
	state, err := vmDir.State()
	if err != nil {
		return ListVMInfo{}, err
	}

	return ListVMInfo{
		Source:   source,
		Name:     name,
		Disk:     diskGB,
		Size:     sizeGB,
		Accessed: c.formatAccessDate(accessDate),
		Running:  running,
		State:    string(state),
	}, nil
}

func SortedInfos(infos []ListVMInfo) []ListVMInfo {
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// formatAccessDate mirrors List.formatAccessDate: relative wording for text
// output, ISO 8601 for JSON.
func (c *ListCommand) formatAccessDate(accessDate time.Time) string {
	if c.ISO8601Dates {
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
