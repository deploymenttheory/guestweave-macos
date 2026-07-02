//go:build darwin

package cli

import (
	"context"
	"fmt"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newListCommand() *cobra.Command {
	c := &weavecommand.ListCommand{}
	var (
		format string
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List local VMs and cached remote images",
		Example: `  weave list
  weave list --source local --format json
  weave list -q`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			parsedFormat, ok := ParseFormat(format)
			if !ok {
				return fmt.Errorf("unsupported format: %q", format)
			}
			c.ISO8601Dates = parsedFormat == FormatJSON
			if err := c.Validate(); err != nil {
				return err
			}
			return runBackground(cmd, func(ctx context.Context) error {
				infos, err := c.Infos(ctx)
				if err != nil {
					return err
				}
				if quiet {
					for _, info := range infos {
						fmt.Println(info.Name)
					}
					return nil
				}
				anyInfos := make([]any, 0, len(infos))
				for _, info := range infos {
					anyInfos = append(anyInfos, info)
				}
				fmt.Println(parsedFormat.RenderList(anyInfos))
				return nil
			})
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&c.Source, "source", "", "restrict the listing to one source (local|oci)")
	flags.StringVar(&format, "format", "text", "output format (text|json)")
	flags.BoolVarP(&quiet, "quiet", "q", false, "print VM names only")
	return cmd
}
