//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	"github.com/spf13/cobra"
)

func newSetupCommand() *cobra.Command {
	c := &weavecommand.SetupCommand{}

	cmd := &cobra.Command{
		Use:   "setup <name>",
		Short: "Run in-guest setup automation against a VM",
		Long: `Run in-guest setup automation against a VM. Preset mode drives the
guest with a scripted unattended preset; agent mode drives it with a
Claude-based computer-use agent.`,
		Example: `  weave setup sequoia --unattended default
  weave setup sequoia --mode agent --anthropic-key $ANTHROPIC_API_KEY`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeLocalMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			c.Name = args[0]
			if err := c.Validate(); err != nil {
				return err
			}
			return runBackground(cmd, c.Run)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&c.Mode, "mode", "preset", "automation mode (preset|agent)")
	flags.StringVar(&c.Unattended, "unattended", "", "preset name or YAML path (preset mode)")
	flags.StringVar(&c.AnthropicKey, "anthropic-key", "", "Anthropic API key (agent mode; falls back to ANTHROPIC_API_KEY)")
	flags.StringVar(&c.Model, "model", "claude-sonnet-4-6", "Claude model to drive agent mode with")
	flags.IntVar(&c.MaxIterations, "max-iterations", 200, "maximum agent iterations")
	flags.StringVar(&c.SystemPrompt, "system-prompt", "", "extra system prompt for agent mode")
	flags.BoolVar(&c.Debug, "debug", false, "save per-step screenshots and agent transcripts")
	flags.StringVar(&c.DebugDir, "debug-dir", "", "directory for debug artifacts")
	flags.BoolVar(&c.ShowScreen, "show-screen", false, "open a view-only browser viewer of the VM screen")
	return cmd
}
