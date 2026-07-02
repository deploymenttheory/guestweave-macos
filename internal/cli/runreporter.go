// The default run.Reporter: stdout/stderr lines plus the ui package's state
// hooks, preserving the run workflow's historical output byte-for-byte.
//go:build darwin

package cli

import (
	"fmt"
	"os"

	"github.com/deploymenttheory/guestweave/internal/clipboard"
	"github.com/deploymenttheory/guestweave/internal/ui"
	weavevm "github.com/deploymenttheory/guestweave/internal/vm"
)

type runReporter struct{}

func (runReporter) Linef(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

func (runReporter) Errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func (runReporter) RunInfoResolved(info ui.RunInfo)       { ui.SetRunInfo(info) }
func (runReporter) VNCURLPublished(url string)            { ui.SetVNCURL(url) }
func (runReporter) OpenURL(url string)                    { ui.OpenURL(url) }
func (runReporter) ClipboardHealth(h clipboard.Health)    { ui.SetClipboardHealth(h) }
func (runReporter) VMSwapped(vm *weavevm.VM)              { ui.SwapVM(vm) }
func (runReporter) BindRevertHandler(h func(string) bool) { ui.RevertFunc = h }
