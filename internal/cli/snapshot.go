//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage VM snapshots",
		Long: `Manage VM snapshots. Snapshots of a running VM are taken live through
its run process; otherwise the VM's disk is snapshotted directly.`,
	}

	create := &weavecommand.SnapshotCreateCommand{}
	createCmd := &cobra.Command{
		Use:               "create <vm> <snapshot-name>",
		Short:             "Create a snapshot of a VM",
		Example:           `  weave snapshot create sequoia clean-install -d "fresh macOS"`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeLocalMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			create.VM, create.Name = args[0], args[1]
			return runBackground(cmd, create.Run)
		},
	}
	createCmd.Flags().StringVarP(&create.Description, "description", "d", "", "free-form snapshot description")

	listCmd := &cobra.Command{
		Use:               "list <vm>",
		Aliases:           []string{"ls"},
		Short:             "List a VM's snapshots",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeLocalMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.SnapshotListCommand{VM: args[0]}
			return runBackground(cmd, c.Run)
		},
	}

	revertCmd := &cobra.Command{
		Use:               "revert <vm> <snapshot-name>",
		Aliases:           []string{"restore"},
		Short:             "Revert a VM to a snapshot",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeLocalMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.SnapshotRevertCommand{VM: args[0], Ref: args[1]}
			return runBackground(cmd, c.Run)
		},
	}

	deleteCmd := &cobra.Command{
		Use:               "delete <vm> <snapshot-name>",
		Aliases:           []string{"rm"},
		Short:             "Delete a VM snapshot",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeLocalMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.SnapshotDeleteCommand{VM: args[0], Ref: args[1]}
			return runBackground(cmd, c.Run)
		},
	}

	cmd.AddCommand(createCmd, listCmd, revertCmd, deleteCmd)
	return cmd
}
