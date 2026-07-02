// Shell completion: VM name listers (ports of tart's
// ShellCompletions.swift) and their cobra ValidArgsFunction adapters.
//go:build darwin

package cli

import (
	"strings"

	"github.com/deploymenttheory/guestweave/internal/vm/layout"
	vmstorage "github.com/deploymenttheory/guestweave/internal/vm/storage"
	"github.com/spf13/cobra"
)

// normalizeName escapes colons, which are misinterpreted by Zsh completion.
func normalizeName(name string) string {
	return strings.ReplaceAll(name, ":", "\\:")
}

// listMachines ports completeMachines(_:_:_:): local and OCI-cached VMs.
func listMachines() []string {
	var names []string

	if localStorage, err := vmstorage.NewVMStorageLocal(); err == nil {
		if entries, err := localStorage.List(); err == nil {
			for _, entry := range entries {
				names = append(names, normalizeName(entry.Name))
			}
		}
	}
	if ociStorage, err := vmstorage.NewVMStorageOCI(); err == nil {
		if entries, err := ociStorage.List(); err == nil {
			for _, entry := range entries {
				names = append(names, normalizeName(entry.Name))
			}
		}
	}
	return names
}

// listLocalMachines ports completeLocalMachines(_:_:_:).
func listLocalMachines() []string {
	var names []string
	if localStorage, err := vmstorage.NewVMStorageLocal(); err == nil {
		if entries, err := localStorage.List(); err == nil {
			for _, entry := range entries {
				names = append(names, normalizeName(entry.Name))
			}
		}
	}
	return names
}

// listRunningMachines ports completeRunningMachines(_:_:_:).
func listRunningMachines() []string {
	var names []string
	if localStorage, err := vmstorage.NewVMStorageLocal(); err == nil {
		if entries, err := localStorage.List(); err == nil {
			for _, entry := range entries {
				if state, err := entry.VMDir.State(); err == nil && state == layout.VMDirectoryStateRunning {
					names = append(names, normalizeName(entry.Name))
				}
			}
		}
	}
	return names
}

// completeMachines offers local and OCI-cached VM names for the first
// positional argument.
func completeMachines(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return listMachines(), cobra.ShellCompDirectiveNoFileComp
}

// completeLocalMachines offers local VM names for the first positional
// argument.
func completeLocalMachines(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return listLocalMachines(), cobra.ShellCompDirectiveNoFileComp
}

// completeRunningMachines offers running VM names for the first positional
// argument.
func completeRunningMachines(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return listRunningMachines(), cobra.ShellCompDirectiveNoFileComp
}
