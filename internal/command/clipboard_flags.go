// Shared clipboard-policy flag plumbing for the run, set, and config commands:
// the raw CLI flag values and their translation into a clipboardpolicy.Override.
//go:build darwin

package command

import (
	"strings"

	"github.com/deploymenttheory/weave/internal/clipboardpolicy"
)

// clipboardFlagValues holds the raw clipboard-policy flag values shared by the
// run, set, and config commands. Empty string / zero means "flag not supplied".
type clipboardFlagValues struct {
	Enabled      string // on|off
	Direction    string // disabled|bidirectional|hostToGuest|guestToHost
	Formats      string // csv of text,rich,image
	Files        string // on|off
	AllowedTypes string // csv of canonical types, e.g. "text/html,text/plain"
	Audit        string // on|off
	SessionMbps  int
	BandwidthPct int
	MaxBytes     int64
}

// override translates the supplied flags into a clipboardpolicy.Override; unset
// flags leave the corresponding field nil so they inherit the underlying policy.
func (v clipboardFlagValues) override() clipboardpolicy.Override {
	o := clipboardpolicy.Override{}
	if v.Enabled != "" {
		enabled := isOn(v.Enabled)
		o.Enabled = &enabled
	}
	if v.Direction != "" {
		direction := clipboardpolicy.Direction(v.Direction)
		o.Direction = &direction
	}
	if v.Formats != "" {
		set := parseCSVSet(v.Formats)
		plain, rich, image := set["text"], set["rich"], set["image"]
		o.PlainText, o.RichText, o.Image = &plain, &rich, &image
	}
	if v.Files != "" {
		files := isOn(v.Files)
		o.FileTransfer = &files
	}
	if v.AllowedTypes != "" {
		o.AllowedTypes = parseCSVList(v.AllowedTypes)
	}
	if v.Audit != "" {
		audit := isOn(v.Audit)
		o.AuditLog = &audit
	}
	if v.SessionMbps > 0 {
		mbps := v.SessionMbps
		o.SessionMbps = &mbps
	}
	if v.BandwidthPct > 0 {
		pct := v.BandwidthPct
		o.BandwidthPct = &pct
	}
	if v.MaxBytes > 0 {
		max := v.MaxBytes
		o.MaxContentBytes = &max
	}
	return o
}

// parseCSVList splits a comma-separated list into trimmed, non-empty entries,
// preserving case (canonical types such as "text/html" are case-sensitive).
func parseCSVList(csv string) []string {
	var out []string
	for field := range strings.SplitSeq(csv, ",") {
		if field = strings.TrimSpace(field); field != "" {
			out = append(out, field)
		}
	}
	return out
}
