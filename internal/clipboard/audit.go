// Structured clipboard transfer auditing: one JSON record per applied transfer
// or policy rejection, written to the clipboard audit log (and OTel) when the
// policy's AuditLog is on or GUESTWEAVE_CLIPBOARD_AUDIT is set. This is the enterprise
// "who moved what across the boundary" trail, distinct from GUESTWEAVE_CLIPBOARD_DEBUG.
//go:build darwin

package clipboard

import (
	"encoding/json"
	"time"

	"github.com/deploymenttheory/guestweave/internal/clipboard/wire"
	"github.com/deploymenttheory/guestweave/internal/logging"
)

// Audit directions and decisions.
const (
	auditHostToGuest  = "h2g"
	auditGuestToHost  = "g2h"
	auditApplied      = "applied"
	auditBlocked      = "blocked"
	auditPolicyChange = "policy-change"
)

// auditFile is one file named in an audit record.
type auditFile struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

// auditRecord is one structured clipboard audit line (JSON).
type auditRecord struct {
	Time      string      `json:"time"`
	VM        string      `json:"vm"`
	Direction string      `json:"direction"` // h2g | g2h
	Decision  string      `json:"decision"`  // applied | blocked
	Reason    string      `json:"reason,omitempty"`
	Formats   []string    `json:"formats,omitempty"`
	Files     []auditFile `json:"files,omitempty"`
	Bytes     int         `json:"bytes"`
}

// transferRecord builds the audit record for an applied transfer.
func transferRecord(direction string, p wire.Payload) auditRecord {
	rec := auditRecord{Direction: direction, Decision: auditApplied}
	for _, it := range p.Items {
		rec.Formats = append(rec.Formats, string(it.Format))
		rec.Bytes += len(it.Data)
	}
	for _, f := range p.Files {
		rec.Files = append(rec.Files, auditFile{Name: f.Name, Size: len(f.Data)})
		rec.Bytes += len(f.Data)
	}
	return rec
}

// blockedRecord builds the audit record for representations dropped by policy.
func blockedRecord(direction, reason string, dropped []wire.OversizeDrop) auditRecord {
	rec := auditRecord{Direction: direction, Decision: auditBlocked, Reason: reason}
	for _, d := range dropped {
		if d.Name != "" {
			rec.Files = append(rec.Files, auditFile{Name: d.Name, Size: d.Size})
		} else {
			rec.Formats = append(rec.Formats, string(d.Format))
		}
		rec.Bytes += d.Size
	}
	return rec
}

// auditTransfer records an applied transfer (the payload that crossed).
func (e *Engine) auditTransfer(direction string, p wire.Payload) {
	if !e.auditOn {
		return
	}
	e.emitAudit(transferRecord(direction, p))
}

// auditBlocked records representations dropped by policy (e.g. oversize).
func (e *Engine) auditBlocked(direction, reason string, dropped []wire.OversizeDrop) {
	if !e.auditOn || len(dropped) == 0 {
		return
	}
	e.emitAudit(blockedRecord(direction, reason, dropped))
}

// auditPolicyChange records a live policy update (the control-plane action),
// with the new effective policy summary in Reason.
func (e *Engine) auditPolicyChange(summary string) {
	if !e.auditOn {
		return
	}
	e.emitAudit(auditRecord{Decision: auditPolicyChange, Reason: summary})
}

func (e *Engine) emitAudit(rec auditRecord) {
	rec.Time = time.Now().UTC().Format(time.RFC3339Nano)
	rec.VM = e.vmName
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	logging.LogAudit(string(data))
}
