// weave clipboard: inspect and live-update the clipboard policy of a running VM
// over its host control socket (no restart). Persisting also writes the VM's
// stored policy.
//go:build darwin

package command

import (
	"context"

	"github.com/deploymenttheory/guestweave/internal/clipboardctl"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/vmstorage"
)

// ClipboardGetCommand shows the running effective policy of a VM.
type ClipboardGetCommand struct {
	Name string
}

func (c *ClipboardGetCommand) Run(ctx context.Context) error {
	// An empty override is a pure query: the engine returns its current policy.
	return sendClipboardControl(ctx, c.Name, clipboardctl.Request{})
}

// ClipboardSetCommand live-updates a running VM's clipboard policy, optionally
// persisting it onto the VM's config.
type ClipboardSetCommand struct {
	Name    string
	Values  ClipboardFlagValues
	Persist bool
}

func (c *ClipboardSetCommand) Run(ctx context.Context) error {
	override := c.Values.Override()
	if override.IsZero() {
		return weaveerrors.ErrGeneric("weave clipboard set: no policy flags supplied")
	}
	return sendClipboardControl(ctx, c.Name, clipboardctl.Request{Override: override, Persist: c.Persist})
}

// sendClipboardControl dials the named VM's clipboard control socket, sends the
// request, and prints the resulting effective policy.
func sendClipboardControl(ctx context.Context, name string, req clipboardctl.Request) error {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(name)
	if err != nil {
		return err
	}

	resp, err := clipboardctl.Send(ctx, vmDir.ClipboardControlSocketURL(), req)
	if err != nil {
		return weaveerrors.ErrGeneric(
			"clipboard control unreachable for %q: %v (is the VM running with the clipboard enabled?)", name, err)
	}
	if !resp.OK {
		return weaveerrors.ErrGeneric("clipboard update failed: %s", resp.Error)
	}
	if resp.Policy != nil {
		printClipboardPolicy(*resp.Policy)
	}
	return nil
}
