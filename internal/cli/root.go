// The weave command tree. Each verb lives in its own file as a
// newXxxCommand constructor; this file assembles the tree and owns the
// root-level presentation (banner help, version template).
//go:build darwin

package cli

import (
	"fmt"

	"github.com/deploymenttheory/guestweave/internal/ci"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
	"github.com/deploymenttheory/guestweave/internal/terminal"
	"github.com/spf13/cobra"
)

// NewRootCommand assembles the full weave command tree. The command
// vocabulary stays "weave <subcommand>" even though the binary is named
// guestweave.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:     "weave",
		Short:   "macOS & Linux VM CLI and server",
		Version: ci.CIVersion(),
		// Errors are printed exactly once by Execute (or by the per-command
		// lifecycle), matching the previous dispatcher's terse output.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")

	// Root help keeps the Weave banner above cobra's generated usage.
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if !cmd.HasParent() {
			terminal.PrintBanner(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout())
		}
		defaultHelp(cmd, args)
	})

	root.AddCommand(
		newCreateCommand(),
		newCloneCommand(),
		newRunCommand(),
		newSetCommand(),
		newGetCommand(),
		newListCommand(),
		newLoginCommand(),
		newLogoutCommand(),
		newIPCommand(),
		newExecCommand(),
		newSSHCommand(),
		newPullCommand(),
		newPushCommand(),
		newImportCommand(),
		newExportCommand(),
		newPruneCommand(),
		newRenameCommand(),
		newStopCommand(),
		newDeleteCommand(),
		newFQNCommand(),
		newSnapshotCommand(),
		newIPSWCommand(),
		newImagesCommand(),
		newLogsCommand(),
		newConfigCommand(),
		newClipboardCommand(),
		newServeCommand(),
		newSetupCommand(),
		newHvmmCommand(),
		newVersionCommand(),
	)
	// suspend requires the macOS 14 save-state APIs; registering it
	// conditionally preserves "unknown command" on older hosts (Root.main
	// appends it conditionally in the Swift original).
	if weaveplatform.MacOSAtLeast(14) {
		root.AddCommand(newSuspendCommand())
	}
	return root
}

// newVersionCommand keeps `weave version` working alongside `--version`.
func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the weave version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), ci.CIVersion())
		},
	}
}
