// The presentation seam: the run workflow reports observable events through
// a Reporter instead of printing or driving the ui package directly. The
// default implementation (internal/cli) prints to stdout/stderr and wires the
// ui package's hooks; tests can substitute a recorder. Process control
// (os.Exit, telemetry flush) and the main-thread loop entries
// (ui.RunHeadless / ui.Window.Run) stay in this package — they are the run
// contract, not presentation.
//go:build darwin

package run

import (
	"github.com/deploymenttheory/guestweave/internal/clipboard"
	"github.com/deploymenttheory/guestweave/internal/ui"
	weavevm "github.com/deploymenttheory/guestweave/internal/vm"
)

// Reporter receives the run workflow's observable events.
type Reporter interface {
	// Linef prints one operator-facing status line (CLI: stdout + newline).
	Linef(format string, args ...any)
	// Errorf prints one non-fatal error line (CLI: stderr + newline).
	Errorf(format string, args ...any)
	// RunInfoResolved publishes the launch-time options (CLI: ui.SetRunInfo).
	RunInfoResolved(info ui.RunInfo)
	// VNCURLPublished announces the VNC endpoint (CLI: ui.SetVNCURL).
	VNCURLPublished(url string)
	// OpenURL opens url in the user's default handler (CLI: ui.OpenURL).
	OpenURL(url string)
	// ClipboardHealth relays clipboard-engine health snapshots (CLI:
	// ui.SetClipboardHealth).
	ClipboardHealth(h clipboard.Health)
	// VMSwapped re-points any UI at a rebuilt VM after an in-process snapshot
	// revert (CLI: ui.SwapVM).
	VMSwapped(vm *weavevm.VM)
	// BindRevertHandler wires the UI's snapshot-revert trigger (CLI:
	// ui.RevertFunc = h). Must be called before the window run loop starts.
	BindRevertHandler(h func(ref string) bool)
}

// nopReporter is the defensive default when no Reporter is supplied; the CLI
// always provides one.
type nopReporter struct{}

func (nopReporter) Linef(string, ...any)                {}
func (nopReporter) Errorf(string, ...any)               {}
func (nopReporter) RunInfoResolved(ui.RunInfo)          {}
func (nopReporter) VNCURLPublished(string)              {}
func (nopReporter) OpenURL(string)                      {}
func (nopReporter) ClipboardHealth(clipboard.Health)    {}
func (nopReporter) VMSwapped(*weavevm.VM)               {}
func (nopReporter) BindRevertHandler(func(string) bool) {}
