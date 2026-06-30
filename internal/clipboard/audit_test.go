//go:build darwin

package clipboard

import (
	"testing"

	"github.com/deploymenttheory/weave/internal/clipboard/wire"
)

func TestTransferRecord(t *testing.T) {
	p := wire.Payload{
		Items: []wire.DataItem{{Format: wire.CanonPlainText, Data: []byte("hello")}}, // 5 B
		Files: []wire.DataFile{{Name: "a.txt", Data: []byte("file!")}},               // 5 B
	}
	rec := transferRecord(auditHostToGuest, p)
	if rec.Direction != auditHostToGuest || rec.Decision != auditApplied {
		t.Errorf("direction/decision = %s/%s", rec.Direction, rec.Decision)
	}
	if len(rec.Formats) != 1 || rec.Formats[0] != string(wire.CanonPlainText) {
		t.Errorf("formats = %v", rec.Formats)
	}
	if len(rec.Files) != 1 || rec.Files[0].Name != "a.txt" || rec.Files[0].Size != 5 {
		t.Errorf("files = %+v", rec.Files)
	}
	if rec.Bytes != 10 {
		t.Errorf("bytes = %d, want 10", rec.Bytes)
	}
}

func TestBlockedRecord(t *testing.T) {
	dropped := []wire.OversizeDrop{
		{Format: wire.CanonTIFF, Size: 4096},
		{Name: "big.bin", Size: 9000},
	}
	rec := blockedRecord(auditGuestToHost, "oversize", dropped)
	if rec.Decision != auditBlocked || rec.Reason != "oversize" {
		t.Errorf("decision/reason = %s/%s", rec.Decision, rec.Reason)
	}
	if len(rec.Formats) != 1 || rec.Formats[0] != string(wire.CanonTIFF) {
		t.Errorf("formats = %v", rec.Formats)
	}
	if len(rec.Files) != 1 || rec.Files[0].Name != "big.bin" || rec.Files[0].Size != 9000 {
		t.Errorf("files = %+v", rec.Files)
	}
	if rec.Bytes != 13096 {
		t.Errorf("bytes = %d, want 13096", rec.Bytes)
	}
}
