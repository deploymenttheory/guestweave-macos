// weave clipboard: inspect and live-update the clipboard policy of a running VM
// over its host control socket (no restart). Persisting also writes the VM's
// stored policy. Subcommands:
//
//	clipboard get <name>                       show the running effective policy
//	clipboard set <name> [--persist] \
//	    [--enabled on|off] [--direction ...] [--formats text,rich,image] \
//	    [--files on|off] [--allowed-types text/html,...] [--audit on|off] \
//	    [--session-mbps N] [--bandwidth-pct N] [--max-bytes N]
//go:build darwin

package command

import (
	"context"

	"github.com/deploymenttheory/guestweave/internal/clipboardctl"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/vmstorage"
)

// ClipboardCommand carries the raw subcommand arguments; dispatch happens in Run.
type ClipboardCommand struct {
	Args []string
}

const clipboardUsage = "usage: weave clipboard <get|set> <name> ..."

func (c *ClipboardCommand) Run(ctx context.Context) error {
	if len(c.Args) == 0 {
		return weaveerrors.ErrGeneric(clipboardUsage)
	}
	verb, rest := c.Args[0], c.Args[1:]
	switch verb {
	case "get":
		return c.runGet(ctx, rest)
	case "set":
		return c.runSet(ctx, rest)
	default:
		return weaveerrors.ErrGeneric(clipboardUsage)
	}
}

func (c *ClipboardCommand) runGet(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return weaveerrors.ErrGeneric("usage: weave clipboard get <name>")
	}
	// An empty override is a pure query: the engine returns its current policy.
	return sendClipboardControl(ctx, args[0], clipboardctl.Request{})
}

func (c *ClipboardCommand) runSet(ctx context.Context, args []string) error {
	var v clipboardFlagValues
	var persist bool
	fs := NewFlagSet("clipboard set")
	fs.StringVar(&v.Enabled, "enabled", "", "on|off")
	fs.StringVar(&v.Direction, "direction", "", "disabled|bidirectional|hostToGuest|guestToHost")
	fs.StringVar(&v.Formats, "formats", "", "csv of text,rich,image")
	fs.StringVar(&v.Files, "files", "", "on|off")
	fs.StringVar(&v.AllowedTypes, "allowed-types", "", "csv of canonical types, e.g. text/html")
	fs.StringVar(&v.Audit, "audit", "", "on|off")
	fs.IntVar(&v.SessionMbps, "session-mbps", 0, "declared session bandwidth (Mbps)")
	fs.IntVar(&v.BandwidthPct, "bandwidth-pct", 0, "percent of session bandwidth for clipboard")
	fs.Int64Var(&v.MaxBytes, "max-bytes", 0, "per-item/file size cap in bytes")
	fs.BoolVar(&persist, "persist", false, "also write the policy to the VM config")
	positionals, err := ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return weaveerrors.ErrGeneric("usage: weave clipboard set <name> [flags]")
	}

	override := v.override()
	if override.IsZero() {
		return weaveerrors.ErrGeneric("weave clipboard set: no policy flags supplied")
	}
	return sendClipboardControl(ctx, positionals[0], clipboardctl.Request{Override: override, Persist: persist})
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
