// Package snapshot owns VM disk snapshots: named, point-in-time copies of a
// VM's disk image and persistent firmware (nvram / EFI vars), stored under
// <vmdir>/snapshots. These are independent of the VZ save/restore "suspend"
// state (state.vzvmsave): a snapshot captures disk state, imposes no
// constraints on the running VM configuration, and reverting restores the
// disk so the VM boots from that point. Copies use APFS copy-on-write
// (fsutil.CloneFile), so snapshots are near-instant and claim no extra space
// until written.
//
// Snapshots of a *running* VM go through the run process (only it owns the VZ
// handle) via the Unix socket protocol in socket.go; the VM layer's
// CreateSnapshotPaused wires the live RAM/device capture through
// CreateOptions.SaveState.
//go:build darwin

package snapshot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/fsutil"
	"github.com/deploymenttheory/guestweave/internal/vmdirectory"
)

// MaxSnapshots is the maximum number of disk snapshots retained per VM.
const MaxSnapshots = 10

// Snapshot is one named, point-in-time snapshot. It always captures the disk and
// firmware; HasState additionally records that a full live RAM/device state
// (state.vzvmsave) was captured, so reverting resumes the exact running moment
// instead of rebooting from disk.
type Snapshot struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	HasState    bool      `json:"has_state"`
}

// CreateOptions parameterises Create.
type CreateOptions struct {
	Name        string
	Description string
	// ExtraRequiredBytes is added to the free-space guard's requirement — used
	// to reserve room for the RAM state file (≈ the VM's memory size).
	ExtraRequiredBytes int64
	// SaveState, when non-nil, is called with the destination path for the live
	// RAM/device state (state.vzvmsave); the VM layer wires this to
	// SaveMachineStateTo. nil produces a disk-only snapshot (e.g. a stopped VM).
	SaveState func(destPath string) error
}

// snapshotIndex is the on-disk catalogue (snapshots/index.json).
type snapshotIndex struct {
	Snapshots []Snapshot `json:"snapshots"`
}

// Dir is the directory holding every snapshot payload and the index.
func Dir(d *vmdirectory.VMDirectory) string { return filepath.Join(d.BaseURL, "snapshots") }

func indexURL(d *vmdirectory.VMDirectory) string {
	return filepath.Join(Dir(d), "index.json")
}

func payloadDir(d *vmdirectory.VMDirectory, id string) string {
	return filepath.Join(Dir(d), id)
}

// requiredBytes is the worst-case space a snapshot needs: a full copy of
// the disk image plus firmware. APFS copy-on-write usually makes the clone
// near-free, but this is the safe upper bound used by the free-space guard.
func requiredBytes(d *vmdirectory.VMDirectory) (int64, error) {
	total, err := fsutil.AllocatedSizeBytes(d.DiskURL())
	if err != nil {
		return 0, err
	}
	if fwPath, _ := d.FirmwareFile(); fsutil.Exists(fwPath) {
		if fw, err := fsutil.AllocatedSizeBytes(fwPath); err == nil {
			total += fw
		}
	}
	return total, nil
}

func readIndex(d *vmdirectory.VMDirectory) (snapshotIndex, error) {
	var idx snapshotIndex
	data, err := os.ReadFile(indexURL(d))
	if errors.Is(err, os.ErrNotExist) {
		return snapshotIndex{}, nil
	}
	if err != nil {
		return idx, err
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return idx, weaveerrors.ErrGeneric("the snapshots index is corrupt: %v", err)
	}
	return idx, nil
}

func writeIndex(d *vmdirectory.VMDirectory, idx snapshotIndex) error {
	if err := os.MkdirAll(Dir(d), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(indexURL(d), data, 0o644)
}

func findSnapshot(idx snapshotIndex, ref string) (int, bool) {
	for i, s := range idx.Snapshots {
		if s.ID == ref || strings.EqualFold(s.Name, ref) {
			return i, true
		}
	}
	return -1, false
}

// List returns the VM's snapshots in creation order.
func List(d *vmdirectory.VMDirectory) ([]Snapshot, error) {
	idx, err := readIndex(d)
	if err != nil {
		return nil, err
	}
	return idx.Snapshots, nil
}

// Create clones the VM's current disk and firmware into a new named
// snapshot, and — when opts.SaveState is set — also captures the live RAM/device
// state. The caller quiesces the disk first (the VM must be stopped, or paused
// via the run process) so the image is consistent. Enforces unique names, the
// MaxSnapshots cap, and a free-space guard.
func Create(d *vmdirectory.VMDirectory, opts CreateOptions) (Snapshot, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return Snapshot{}, weaveerrors.ErrGeneric("a snapshot name is required")
	}

	idx, err := readIndex(d)
	if err != nil {
		return Snapshot{}, err
	}
	if _, exists := findSnapshot(idx, name); exists {
		return Snapshot{}, weaveerrors.ErrGeneric("a snapshot named %q already exists", name)
	}
	if len(idx.Snapshots) >= MaxSnapshots {
		return Snapshot{}, weaveerrors.ErrGeneric(
			"snapshot limit reached (%d); delete one before creating another", MaxSnapshots)
	}

	// Free-space guard: refuse if the volume couldn't hold a full copy of the
	// disk + firmware (+ the RAM state, when captured). Copy-on-write usually
	// makes the disk clone near-free, but this keeps a snapshot from being
	// created on a volume too full to hold its eventual divergence, a non-CoW
	// fallback copy, or the state file.
	required, err := requiredBytes(d)
	if err != nil {
		return Snapshot{}, err
	}
	required += opts.ExtraRequiredBytes
	available, err := fsutil.AvailableBytes(d.BaseURL)
	if err != nil {
		return Snapshot{}, err
	}
	if available < required {
		return Snapshot{}, weaveerrors.ErrGeneric(
			"not enough free space to snapshot %q: need %s, only %s available",
			name, fsutil.ByteCountString(required), fsutil.ByteCountString(available))
	}

	id := fsutil.UUID()
	payload := payloadDir(d, id)
	if err := os.MkdirAll(payload, 0o755); err != nil {
		return Snapshot{}, err
	}
	cleanup := func() { _ = os.RemoveAll(payload) }

	if err := fsutil.CloneFile(d.DiskURL(), filepath.Join(payload, "disk.img")); err != nil {
		cleanup()
		return Snapshot{}, err
	}
	if fwPath, fwName := d.FirmwareFile(); fsutil.Exists(fwPath) {
		if err := fsutil.CloneFile(fwPath, filepath.Join(payload, fwName)); err != nil {
			cleanup()
			return Snapshot{}, err
		}
	}

	hasState := false
	if opts.SaveState != nil {
		if err := opts.SaveState(statePath(d, id)); err != nil {
			cleanup()
			return Snapshot{}, weaveerrors.ErrGeneric("failed to capture VM state: %v", err)
		}
		hasState = true
	}

	snap := Snapshot{
		ID:          id,
		Name:        name,
		Description: opts.Description,
		CreatedAt:   time.Now().UTC(),
		HasState:    hasState,
	}
	idx.Snapshots = append(idx.Snapshots, snap)
	if err := writeIndex(d, idx); err != nil {
		cleanup()
		return Snapshot{}, err
	}
	return snap, nil
}

func statePath(d *vmdirectory.VMDirectory, id string) string {
	return filepath.Join(payloadDir(d, id), "state.vzvmsave")
}

// ByRef returns the snapshot matching ref (name or id).
func ByRef(d *vmdirectory.VMDirectory, ref string) (Snapshot, bool, error) {
	idx, err := readIndex(d)
	if err != nil {
		return Snapshot{}, false, err
	}
	if i, ok := findSnapshot(idx, ref); ok {
		return idx.Snapshots[i], true, nil
	}
	return Snapshot{}, false, nil
}

// Revert restores the VM's disk and firmware from the named snapshot
// (matched by name or id), and stages its RAM state (if any) at the VM's state
// path so the next start resumes the exact moment rather than rebooting. The VM
// must be stopped — the live disk is replaced. Returns whether RAM state was
// staged.
func Revert(d *vmdirectory.VMDirectory, ref string) (restoredState bool, err error) {
	idx, err := readIndex(d)
	if err != nil {
		return false, err
	}
	i, ok := findSnapshot(idx, ref)
	if !ok {
		return false, weaveerrors.ErrGeneric("no snapshot named %q", ref)
	}
	snap := idx.Snapshots[i]
	payload := payloadDir(d, snap.ID)

	diskSrc := filepath.Join(payload, "disk.img")
	if !fsutil.Exists(diskSrc) {
		return false, weaveerrors.ErrGeneric("snapshot %q is missing its disk image", snap.Name)
	}
	if err := fsutil.CloneFile(diskSrc, d.DiskURL()); err != nil {
		return false, err
	}
	if fwPath, fwName := d.FirmwareFile(); fsutil.Exists(filepath.Join(payload, fwName)) {
		if err := fsutil.CloneFile(filepath.Join(payload, fwName), fwPath); err != nil {
			return false, err
		}
	}

	// Stage (or clear) the RAM state so the next start resumes from it (or boots
	// fresh for a disk-only snapshot).
	stateSrc := statePath(d, snap.ID)
	if snap.HasState && fsutil.Exists(stateSrc) {
		if err := fsutil.CloneFile(stateSrc, d.StateURL()); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := os.RemoveAll(d.StateURL()); err != nil {
		return false, err
	}
	return false, nil
}

// Delete removes the named snapshot (matched by name or id) and its
// payload. Safe at any VM state — it never touches the live disk.
func Delete(d *vmdirectory.VMDirectory, ref string) error {
	idx, err := readIndex(d)
	if err != nil {
		return err
	}
	i, ok := findSnapshot(idx, ref)
	if !ok {
		return weaveerrors.ErrGeneric("no snapshot named %q", ref)
	}
	snap := idx.Snapshots[i]
	if err := os.RemoveAll(payloadDir(d, snap.ID)); err != nil {
		return err
	}
	idx.Snapshots = append(idx.Snapshots[:i], idx.Snapshots[i+1:]...)
	return writeIndex(d, idx)
}
