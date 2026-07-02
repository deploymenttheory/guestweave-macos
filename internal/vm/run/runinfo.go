// Launch-time option summaries for the UI's VM Info panel.
//go:build darwin

package run

import (
	"strings"

	"github.com/deploymenttheory/guestweave/internal/ui"
)

// buildRunInfo collects the applied launch-time options (those not persisted in
// the VM config) for the UI's VM Info panel.
func (c *Session) buildRunInfo() ui.RunInfo {
	return ui.RunInfo{
		Network:     c.networkSummary(),
		Clipboard:   c.clipboardSummary(),
		Disks:       c.Disk,
		Dirs:        c.Dir,
		SharedDirs:  c.SharedDir,
		USBStorage:  c.USBStorage,
		Rosetta:     c.RosettaTag,
		Suspendable: c.Suspendable,
		VNC:         c.VNC || c.VNCExperimental,
		Nested:      c.Nested,
		NoAudio:     c.NoAudio,
		NoTrackpad:  c.NoTrackpad,
		NoKeyboard:  c.NoKeyboard,
		NoPointer:   c.NoPointer,
		CaptureKeys: c.CaptureSystemKeys,
	}
}

func (c *Session) networkSummary() string {
	switch {
	case c.NetHost:
		return "host"
	case len(c.NetBridged) > 0:
		return "bridged: " + strings.Join(c.NetBridged, ", ")
	case c.NetSoftnet:
		return "softnet"
	case len(c.NetDevice) > 0:
		return "device: " + strings.Join(c.NetDevice, ", ")
	case c.NetProfile != "":
		return "profile: " + c.NetProfile
	default:
		return "" // VM Info renders this as "nat (default)"
	}
}

func (c *Session) clipboardSummary() string {
	if !c.clipboardRun || !c.clipboardPolicy.Active() {
		return "disabled"
	}
	parts := []string{string(c.clipboardPolicy.Direction)}
	if c.clipboardPolicy.FileTransfer {
		parts = append(parts, "files")
	}
	if c.clipboardPolicy.AuditLog {
		parts = append(parts, "audit")
	}
	return strings.Join(parts, " · ")
}
