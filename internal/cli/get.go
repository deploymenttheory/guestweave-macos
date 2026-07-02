//go:build darwin

package cli

import (
	"context"
	"fmt"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newGetCommand() *cobra.Command {
	c := &weavecommand.GetCommand{}
	var format string

	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Show a VM's configuration",
		Example: `  weave get sequoia
  weave get sequoia --format json`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			parsedFormat, ok := ParseFormat(format)
			if !ok {
				return fmt.Errorf("unsupported format: %q", format)
			}
			c.Name = args[0]
			return runBackground(cmd, func(ctx context.Context) error {
				info, err := c.Info(ctx)
				if err != nil {
					return err
				}
				fmt.Println(parsedFormat.RenderSingle(info))
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "output format (text|json)")
	return cmd
}
