//go:build darwin

package snapshot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deploymenttheory/guestweave/internal/vmdirectory"
)

// newTestVMDir lays down a minimal VM bundle (disk + nvram) for snapshot tests.
func newTestVMDir(t *testing.T, diskContents string) *vmdirectory.VMDirectory {
	t.Helper()
	base := t.TempDir()
	d := vmdirectory.NewVMDirectory(base)
	if err := os.WriteFile(d.DiskURL(), []byte(diskContents), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	if err := os.WriteFile(d.NvramURL(), []byte("nvram"), 0o644); err != nil {
		t.Fatalf("write nvram: %v", err)
	}
	return d
}

func TestSnapshotCreateListDelete(t *testing.T) {
	d := newTestVMDir(t, "disk-v1")

	if snaps, err := List(d); err != nil || len(snaps) != 0 {
		t.Fatalf("expected no snapshots, got %v err=%v", snaps, err)
	}

	if _, err := Create(d, CreateOptions{Name: "first", Description: "the first one"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	snaps, err := List(d)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %v err=%v", snaps, err)
	}
	if snaps[0].Name != "first" || snaps[0].Description != "the first one" || snaps[0].HasState {
		t.Fatalf("unexpected snapshot metadata: %+v", snaps[0])
	}

	if _, err := os.Stat(filepath.Join(payloadDir(d, snaps[0].ID), "disk.img")); err != nil {
		t.Fatalf("snapshot disk missing: %v", err)
	}

	if err := Delete(d, "first"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if snaps, _ := List(d); len(snaps) != 0 {
		t.Fatalf("expected 0 snapshots after delete, got %d", len(snaps))
	}
}

func TestSnapshotDuplicateNameRejected(t *testing.T) {
	d := newTestVMDir(t, "disk")
	if _, err := Create(d, CreateOptions{Name: "dup"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Create(d, CreateOptions{Name: "dup"}); err == nil {
		t.Fatal("expected duplicate-name error, got nil")
	}
}

func TestSnapshotCapEnforced(t *testing.T) {
	d := newTestVMDir(t, "disk")
	for i := range MaxSnapshots {
		if _, err := Create(d, CreateOptions{Name: string(rune('a' + i))}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := Create(d, CreateOptions{Name: "overflow"}); err == nil {
		t.Fatalf("expected cap (%d) to be enforced", MaxSnapshots)
	}
}

func TestSnapshotRevertRestoresDisk(t *testing.T) {
	d := newTestVMDir(t, "original")
	if _, err := Create(d, CreateOptions{Name: "snap"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := os.WriteFile(d.DiskURL(), []byte("mutated"), 0o644); err != nil {
		t.Fatalf("mutate disk: %v", err)
	}
	restoredState, err := Revert(d, "snap")
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if restoredState {
		t.Fatalf("disk-only snapshot should not report restored RAM state")
	}
	got, err := os.ReadFile(d.DiskURL())
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("revert did not restore disk: got %q", got)
	}
}

func TestSnapshotRevertWithStateStagesStateFile(t *testing.T) {
	d := newTestVMDir(t, "disk")
	if _, err := Create(d, CreateOptions{
		Name: "live",
		SaveState: func(dst string) error {
			return os.WriteFile(dst, []byte("ram-state"), 0o600)
		},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	snap, ok, err := ByRef(d, "live")
	if err != nil || !ok || !snap.HasState {
		t.Fatalf("expected a live snapshot with state, got ok=%v snap=%+v err=%v", ok, snap, err)
	}

	restoredState, err := Revert(d, "live")
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if !restoredState {
		t.Fatal("expected revert to report restored RAM state")
	}
	staged, err := os.ReadFile(d.StateURL())
	if err != nil {
		t.Fatalf("state not staged at VM state path: %v", err)
	}
	if string(staged) != "ram-state" {
		t.Fatalf("unexpected staged state: %q", staged)
	}
}

func TestSnapshotRevertUnknownErrors(t *testing.T) {
	d := newTestVMDir(t, "disk")
	if _, err := Revert(d, "nope"); err == nil {
		t.Fatal("expected error reverting an unknown snapshot")
	}
}
