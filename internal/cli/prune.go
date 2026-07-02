//go:build darwin

package cli

import (
	"context"
	"errors"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newPruneCommand() *cobra.Command {
	c := &weavecommand.PruneCommand{}
	var (
		olderThan   uint
		cacheBudget uint
		spaceBudget uint
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Reclaim disk space from caches and VMs",
		Example: `  weave prune
  weave prune --entries vms --older-than 30
  weave prune --space-budget 100`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if olderThan != 0 {
				c.OlderThan = &olderThan
			}
			if spaceBudget != 0 {
				c.SpaceBudget = &spaceBudget
			}
			if cacheBudget != 0 {
				if c.SpaceBudget != nil {
					return errors.New("--cache-budget is deprecated, please use --space-budget")
				}
				c.SpaceBudget = &cacheBudget
			}
			if err := c.Validate(); err != nil {
				return err
			}
			return runBackground(cmd, func(context.Context) error { return c.Run() })
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&c.Entries, "entries", "caches", "what to prune (caches|vms)")
	flags.UintVar(&olderThan, "older-than", 0, "prune entries not accessed in the given number of days")
	flags.UintVar(&cacheBudget, "cache-budget", 0, "deprecated alias for --space-budget")
	flags.UintVar(&spaceBudget, "space-budget", 0, "prune oldest entries until usage fits the given size in GB")
	flags.BoolVar(&c.GC, "gc", false, "also run garbage collection on the temporary directory")
	_ = flags.MarkDeprecated("cache-budget", "please use --space-budget")
	return cmd
}
