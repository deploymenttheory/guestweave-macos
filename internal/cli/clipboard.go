//go:build darwin

package cli

import (
	"context"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

// clipboardPolicyFlags registers the shared clipboard-policy override flags
// (also used by config clipboard, without a prefix, unlike run/set's
// clipboard-* variants).
func clipboardPolicyFlags(cmd *cobra.Command, v *weavecommand.ClipboardFlagValues) {
	flags := cmd.Flags()
	flags.StringVar(&v.Enabled, "enabled", "", "enable or disable the clipboard (on|off)")
	flags.StringVar(&v.Direction, "direction", "", "clipboard direction (disabled|bidirectional|hostToGuest|guestToHost)")
	flags.StringVar(&v.Formats, "formats", "", "allowed clipboard formats (csv of text,rich,image)")
	flags.StringVar(&v.Files, "files", "", "allow file transfers over the clipboard (on|off)")
	flags.StringVar(&v.AllowedTypes, "allowed-types", "", "allowed file types (csv of canonical types, e.g. text/html)")
	flags.StringVar(&v.Audit, "audit", "", "audit clipboard transfers to the audit log (on|off)")
	flags.IntVar(&v.SessionMbps, "session-mbps", 0, "declared session bandwidth in Mbps")
	flags.IntVar(&v.BandwidthPct, "bandwidth-pct", 0, "percent of session bandwidth for the clipboard")
	flags.Int64Var(&v.MaxBytes, "max-bytes", 0, "per-item/file size cap in bytes")
}

func newClipboardCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clipboard",
		Short: "Inspect or adjust a running VM's clipboard policy",
		Long: `Inspect or adjust a VM's clipboard policy. The policy is applied live
through the VM's run process (no restart); set --persist to also write it
to the VM's config.`,
	}

	getCmd := &cobra.Command{
		Use:               "get <name>",
		Short:             "Show the running effective clipboard policy",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRunningMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &weavecommand.ClipboardGetCommand{Name: args[0]}
			return runBackground(cmd, c.Run)
		},
	}

	set := &weavecommand.ClipboardSetCommand{}
	setCmd := &cobra.Command{
		Use:   "set <name> [flags]",
		Short: "Live-update a running VM's clipboard policy",
		Example: `  weave clipboard set sequoia --direction hostToGuest --formats text,rich
  weave clipboard set sequoia --files off --persist`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRunningMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			set.Name = args[0]
			return runBackground(cmd, set.Run)
		},
	}
	clipboardPolicyFlags(setCmd, &set.Values)
	setCmd.Flags().BoolVar(&set.Persist, "persist", false, "also write the policy to the VM config")

	cmd.AddCommand(getCmd, setCmd)
	return cmd
}

// runConfigVerb wraps a settings verb in the background lifecycle.
func runConfigVerb(cmd *cobra.Command, work func() error) error {
	return runBackground(cmd, func(context.Context) error { return work() })
}
