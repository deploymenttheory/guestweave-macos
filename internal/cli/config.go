//go:build darwin

package cli

import (
	"strconv"

	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/spf13/cobra"
)

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and modify weave's settings",
		Long: `Inspect and modify weave's settings (~/.config/weave/config.yaml):
default storage, named storage locations, cache directory, registry
profiles, network interfaces, logging limits and the default clipboard
policy.`,
	}
	cmd.AddCommand(
		newConfigGetCommand(),
		newConfigStorageCommand(),
		newConfigCacheCommand(),
		newConfigRegistryCommand(),
		newConfigNetworkCommand(),
		newConfigLoggingCommand(),
		newConfigClipboardCommand(),
	)
	return cmd
}

func newConfigGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Show the effective configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigVerb(cmd, weavecommand.ConfigGet)
		},
	}
}

func newConfigStorageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Manage named VM storage locations",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List named storage locations",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runConfigVerb(cmd, weavecommand.ConfigStorageList)
			},
		},
		&cobra.Command{
			Use:   "add <name> <path>",
			Short: "Add a named storage location",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runConfigVerb(cmd, func() error { return weavecommand.ConfigStorageAdd(args[0], args[1]) })
			},
		},
		&cobra.Command{
			Use:   "remove <name>",
			Short: "Remove a named storage location",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runConfigVerb(cmd, func() error { return weavecommand.ConfigStorageRemove(args[0]) })
			},
		},
		&cobra.Command{
			Use:   "default <name>",
			Short: "Set the default storage location",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runConfigVerb(cmd, func() error { return weavecommand.ConfigStorageDefault(args[0]) })
			},
		},
	)
	return cmd
}

func newConfigCacheCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage the download cache",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "dir [path]",
		Short: "Show or set the cache directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigVerb(cmd, func() error {
				if len(args) == 0 {
					return weavecommand.ConfigCacheDirShow()
				}
				return weavecommand.ConfigCacheDirSet(args[0])
			})
		},
	})
	return cmd
}

func newConfigRegistryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Manage OCI registry defaults and profiles",
	}

	profile := weaveconfig.RegistryProfile{}
	addCmd := &cobra.Command{
		Use:     "add <name>",
		Short:   "Add or replace a registry profile",
		Example: `  weave config registry add work --organization my-org --host ghcr.io --default`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if profile.Organization == "" {
				return weaveerrors.ErrGeneric(
					"usage: weave config registry add <name> --organization <org> [--host ghcr.io] [--insecure] [--default]",
				)
			}
			profile.Name = args[0]
			return runConfigVerb(cmd, func() error { return weavecommand.ConfigRegistryAdd(profile) })
		},
	}
	addCmd.Flags().StringVar(&profile.Host, "host", "ghcr.io", "registry host")
	addCmd.Flags().StringVar(&profile.Organization, "organization", "", "registry organization/namespace (required)")
	addCmd.Flags().BoolVar(&profile.IsInsecure, "insecure", false, "connect over plain HTTP")
	addCmd.Flags().BoolVar(&profile.IsDefault, "default", false, "make this the default profile")

	var ghcrHost, ghcrOrganization string
	ghcrCmd := &cobra.Command{
		Use:   "ghcr",
		Short: "Set the legacy registry defaults",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigVerb(cmd, func() error {
				return weavecommand.ConfigRegistryGHCR(ghcrHost, ghcrOrganization)
			})
		},
	}
	ghcrCmd.Flags().StringVar(&ghcrHost, "registry", "ghcr.io", "registry host")
	ghcrCmd.Flags().StringVar(&ghcrOrganization, "organization", "", "registry organization/namespace")

	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show the legacy registry defaults",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runConfigVerb(cmd, weavecommand.ConfigRegistryStatus)
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "List registry profiles",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runConfigVerb(cmd, weavecommand.ConfigRegistryList)
			},
		},
		addCmd,
		&cobra.Command{
			Use:   "remove <name>",
			Short: "Remove a registry profile",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runConfigVerb(cmd, func() error { return weavecommand.ConfigRegistryRemove(args[0]) })
			},
		},
		&cobra.Command{
			Use:   "default <name>",
			Short: "Set the default registry profile",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runConfigVerb(cmd, func() error { return weavecommand.ConfigRegistryDefault(args[0]) })
			},
		},
		ghcrCmd,
	)
	return cmd
}

func newConfigNetworkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Inspect host networking",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "interfaces",
		Short: "List bridgeable network interfaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigVerb(cmd, weavecommand.ConfigNetworkInterfaces)
		},
	})
	return cmd
}

func newConfigLoggingCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logging",
		Short: "Show or set the file logger's limits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigVerb(cmd, weavecommand.ConfigLoggingShow)
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "maxSizeMB [N]",
			Short: "Show or set the per-file log cap in MB (0 = unlimited)",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if len(args) == 0 {
					return runConfigVerb(cmd, weavecommand.ConfigLoggingMaxSizeShow)
				}
				mb, err := strconv.Atoi(args[0])
				if err != nil || mb < 0 {
					return weaveerrors.ErrGeneric("maxSizeMB must be a non-negative integer (0 = unlimited)")
				}
				return runConfigVerb(cmd, func() error { return weavecommand.ConfigLoggingMaxSizeSet(mb) })
			},
		},
		&cobra.Command{
			Use:   "keepRotated [true|false]",
			Short: "Show or set whether rotated log files are kept",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if len(args) == 0 {
					return runConfigVerb(cmd, weavecommand.ConfigLoggingKeepRotatedShow)
				}
				keep, err := strconv.ParseBool(args[0])
				if err != nil {
					return weaveerrors.ErrGeneric("keepRotated must be true or false")
				}
				return runConfigVerb(cmd, func() error { return weavecommand.ConfigLoggingKeepRotatedSet(keep) })
			},
		},
	)
	return cmd
}

func newConfigClipboardCommand() *cobra.Command {
	var values weavecommand.ClipboardFlagValues
	cmd := &cobra.Command{
		Use:   "clipboard",
		Short: "Show or set the default clipboard policy",
		Long: `Show or set the global default clipboard policy applied to VMs that
have no per-VM override. Without flags the effective default is printed.`,
		Example: `  weave config clipboard
  weave config clipboard --direction hostToGuest --formats text,rich
  weave config clipboard reset`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigVerb(cmd, func() error { return weavecommand.ConfigClipboardSet(values) })
		},
	}
	clipboardPolicyFlags(cmd, &values)
	cmd.AddCommand(&cobra.Command{
		Use:   "reset",
		Short: "Reset the default clipboard policy to the built-in default",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigVerb(cmd, weavecommand.ConfigClipboardReset)
		},
	})
	return cmd
}
