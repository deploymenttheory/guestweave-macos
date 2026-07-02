// CLI entry point, called from main() with the main goroutine locked to the
// process's main OS thread (AppKit and Virtualization.framework dispatch
// their work to the main queue).
//go:build darwin

package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
)

// Execute parses os.Args and runs the selected command. Commands that reach
// their RunE exit the process from inside the lifecycle wrapper; only
// parse/validation errors and informational paths (help, version,
// completion) return here.
func Execute() {
	// Disable the default SIGINT handler; cancellation by Ctrl+C is handled
	// explicitly per command (Root.main does signal(SIGINT, SIG_IGN)).
	signal.Ignore(syscall.SIGINT)

	// Initialise the viper-backed configuration (defaults, GUESTWEAVE_* env,
	// config.yaml) before any command work runs. Accessors also load lazily,
	// so this is belt-and-braces for the CLI path.
	weaveconfig.Load()

	if err := NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
