//go:build darwin

package cli

import (
	"context"
	"fmt"

	vmservice "github.com/deploymenttheory/guestweave/internal/vm/service"
	"github.com/spf13/cobra"
)

func newListCommand() *cobra.Command {
	var (
		source string
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
			if err := vmservice.ValidateListSource(source); err != nil {
				return err
			}
			return runBackground(cmd, func(ctx context.Context) error {
				infos, err := vmservice.CollectVMInfos(source, parsedFormat == FormatJSON)
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
	flags.StringVar(&source, "source", "", "restrict the listing to one source (local|oci)")
	flags.StringVar(&format, "format", "text", "output format (text|json)")
	flags.BoolVarP(&quiet, "quiet", "q", false, "print VM names only")
	return cmd
}
