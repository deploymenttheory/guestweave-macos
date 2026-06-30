//go:build darwin

package command

import (
	"testing"

	"github.com/deploymenttheory/weave/internal/clipboardpolicy"
)

func TestClipboardFlagValuesOverrideEmpty(t *testing.T) {
	if !(clipboardFlagValues{}).override().IsZero() {
		t.Error("empty flag values should produce a zero override")
	}
}

func TestClipboardFlagValuesOverrideMapping(t *testing.T) {
	v := clipboardFlagValues{
		Enabled:      "on",
		Direction:    "hostToGuest",
		Formats:      "text,image",
		Files:        "off",
		AllowedTypes: "text/html, text/plain",
		SessionMbps:  100,
		BandwidthPct: 25,
		MaxBytes:     2048,
	}
	o := v.override()
	if o.IsZero() {
		t.Fatal("override should not be zero")
	}

	// Apply onto the permissive default and check the resolved policy.
	p := o.Apply(clipboardpolicy.Default())
	if !p.Enabled {
		t.Error("enabled should be true")
	}
	if p.Direction != clipboardpolicy.DirectionHostToGuest {
		t.Errorf("direction = %s, want hostToGuest", p.Direction)
	}
	if !p.Formats.PlainText || p.Formats.RichText || !p.Formats.Image {
		t.Errorf("formats = %+v, want plain+image only", p.Formats)
	}
	if p.FileTransfer {
		t.Error("file transfer should be off")
	}
	if len(p.AllowedTypes) != 2 || p.AllowedTypes[0] != "text/html" || p.AllowedTypes[1] != "text/plain" {
		t.Errorf("allowedTypes = %v, want [text/html text/plain]", p.AllowedTypes)
	}
	if p.SessionMbps != 100 || p.BandwidthPct != 25 {
		t.Errorf("bandwidth = %d/%d, want 100/25", p.SessionMbps, p.BandwidthPct)
	}
	if p.MaxContentBytes != 2048 {
		t.Errorf("maxContentBytes = %d, want 2048", p.MaxContentBytes)
	}
}

func TestClipboardFlagValuesPartialOverrideInherits(t *testing.T) {
	// Only direction set: everything else must inherit the base policy unchanged.
	base := clipboardpolicy.Policy{
		Enabled:      true,
		Direction:    clipboardpolicy.DirectionBidirectional,
		Formats:      clipboardpolicy.Formats{PlainText: true, RichText: true, Image: true},
		FileTransfer: true,
	}
	o := clipboardFlagValues{Direction: "guestToHost"}.override()
	p := o.Apply(base)
	if p.Direction != clipboardpolicy.DirectionGuestToHost {
		t.Errorf("direction = %s, want guestToHost", p.Direction)
	}
	if !p.FileTransfer || !p.Formats.Image {
		t.Errorf("unset fields should be inherited: %+v", p)
	}
}
