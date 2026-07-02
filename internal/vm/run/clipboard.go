// Clipboard: effective-policy resolution (CLI > per-VM config > settings >
// built-in), the agent's virtio serial channel, and the host control socket
// serving live `weave clipboard set` updates.
//go:build darwin

package run

import (
	"context"
	"os"
	"syscall"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	"github.com/deploymenttheory/guestweave/internal/clipboard"
	"github.com/deploymenttheory/guestweave/internal/clipboardctl"
	"github.com/deploymenttheory/guestweave/internal/clipboardpolicy"
	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/logging"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
)

// suspendVM ports the SIGUSR1 handler: snapshot the VM and shut down.
// resolveClipboard computes the effective enterprise clipboard policy for this
// run (CLI flags > per-VM config > settings default > built-in default) and
// records whether the engine should run plus the guest's OS/arch for agent
// deployment.
//
// The weave-guestd engine is the single clipboard mechanism: it runs by default
// (the built-in policy is enabled + bidirectional) for every guest OS, and owns
// the clipboard so the SPICE agent path is not also wired (vm.go gates SPICE on
// ClipboardPolicyEnabled). --no-clipboard, or a resolved policy that is disabled
// (e.g. settings/per-VM with direction=disabled), turns the clipboard off
// entirely — neither the engine nor SPICE runs.
func (c *Session) resolveClipboard(vmConfig *vmconfig.VMConfig) {
	override := c.ClipboardOverride
	if c.Clipboard {
		enabled := true
		override.Enabled = &enabled
	}

	var settingsDefault *clipboardpolicy.Policy
	if settings, err := weaveconfig.LoadSettings(); err == nil {
		settingsDefault = settings.DefaultClipboardPolicy
	}
	perVM := vmConfig.ClipboardPolicy

	policy := clipboardpolicy.Resolve(settingsDefault, perVM, override)
	c.clipboardPolicy = policy
	c.clipboardRun = !c.NoClipboard && policy.Active()
	c.guestGOOS = string(vmConfig.OS)
	c.guestGOARCH = string(vmConfig.Arch)
}

// serveClipboardControl runs the host control socket that lets `weave clipboard
// set` push live policy overrides onto this VM's running engine. Each request
// layers its override onto the engine's current policy, applies it live, and —
// when persist is set — writes the resulting policy to the VM config in place
// (a rename would invalidate this process's fcntl run-lock on config.json).
func (c *Session) serveClipboardControl(ctx context.Context, vmDir *layout.VMDirectory, engine *clipboard.Engine) {
	handler := func(req clipboardctl.Request) (clipboardpolicy.Policy, error) {
		// An empty override is a pure query (weave clipboard get): return the
		// current policy without touching the engine.
		if req.Override.IsZero() {
			return engine.Policy(), nil
		}
		updated := req.Override.Apply(engine.Policy())
		engine.SetPolicy(updated)
		if req.Persist {
			vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
			if err != nil {
				return updated, err
			}
			vmConfig.ClipboardPolicy = &updated
			if err := vmConfig.SaveInPlace(vmDir.ConfigURL()); err != nil {
				return updated, err
			}
		}
		return updated, nil
	}
	if err := clipboardctl.Serve(ctx, vmDir.ClipboardControlSocketURL(), handler); err != nil && ctx.Err() == nil {
		logging.LogError("clipboard control socket: %v", err)
	}
}

// clipboardAgentSerialPort builds the dedicated virtio serial port the resident
// clipboard agent talks over, bridged to the host with two pipes (mirroring
// VirtualBuddy's Pipe()-backed VZFileHandleSerialPortAttachment). The host ends
// are stored on c and reused across VM rebuilds; fresh FileHandles wrap the same
// fds each build (an attachment consumes its handles).
//
// Per VZFileHandleSerialPortAttachment semantics — data written to
// fileHandleForReading goes to the guest, guest output appears on
// fileHandleForWriting — the VM reads the host→guest pipe and writes the
// guest→host pipe; the host writes the former and reads the latter.
func (c *Session) clipboardAgentSerialPort() (idvirt.SerialPortConfigurationProvider, error) {
	if c.clipSerialHostR == nil || c.clipSerialHostW == nil {
		var h2g, g2h [2]int // [read, write]
		if err := syscall.Pipe(h2g[:]); err != nil {
			return nil, weaveerrors.ErrVMConfigurationError("clipboard serial pipe: %v", err)
		}
		if err := syscall.Pipe(g2h[:]); err != nil {
			return nil, weaveerrors.ErrVMConfigurationError("clipboard serial pipe: %v", err)
		}
		// VM ends stay raw/blocking (handed to the framework as bare fds); host
		// ends become *os.File so Go's runtime poller drives their I/O.
		c.clipSerialVMReadFD = h2g[0]  // framework reads -> to guest
		c.clipSerialVMWriteFD = g2h[1] // framework writes guest output here
		c.clipSerialHostW = os.NewFile(uintptr(h2g[1]), "weave-clip-h2g-w")
		c.clipSerialHostR = os.NewFile(uintptr(g2h[0]), "weave-clip-g2h-r")
	}
	ttyRead := foundation.NewFileHandleWithFileDescriptorCloseOnDealloc(c.clipSerialVMReadFD, false)
	ttyWrite := foundation.NewFileHandleWithFileDescriptorCloseOnDealloc(c.clipSerialVMWriteFD, false)
	return createSerialPortConfiguration(ttyRead, ttyWrite), nil
}
