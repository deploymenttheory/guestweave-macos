//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newLogsCommand() *cobra.Command {
	c := &weavecommand.LogsCommand{}

	cmd := &cobra.Command{
		Use:   "logs <info|error|all> | logs clear",
		Short: "Print, follow or clear weave's log files",
		Example: `  weave logs error
  weave logs all --lines 200
  weave logs info -f
  weave logs clear`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"info", "error", "all", "clear"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] == "clear" {
				c.Clear = true
			} else {
				c.Type = args[0]
				if err := c.Validate(); err != nil {
					return err
				}
			}
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.IntVar(&c.Lines, "lines", 0, "print only the last N lines (0 = whole file)")
	flags.BoolVarP(&c.Follow, "follow", "f", false, "keep the file open and stream new entries")
	return cmd
}
