// Disk snapshots: named, point-in-time copies of a VM's disk image and
// persistent firmware (nvram / EFI vars), stored under <vmdir>/snapshots. These
// are independent of the VZ save/restore "suspend" state (state.vzvmsave): a
// snapshot captures disk state, imposes no constraints on the running VM
// configuration, and reverting restores the disk so the VM boots from that
// point. Copies use APFS copy-on-write (fsutil.CloneFile), so snapshots are
// near-instant and claim no extra space until written.
//go:build darwin

package vmdirectory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/fsutil"
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

// SnapshotCreateOptions parameterises CreateSnapshot.
type SnapshotCreateOptions struct {
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

// SnapshotsDir is the directory holding every snapshot payload and the index.
func (d *VMDirectory) SnapshotsDir() string { return filepath.Join(d.BaseURL, "snapshots") }

func (d *VMDirectory) snapshotIndexURL() string {
	return filepath.Join(d.SnapshotsDir(), "index.json")
}

func (d *VMDirectory) snapshotPayloadDir(id string) string {
	return filepath.Join(d.SnapshotsDir(), id)
}

// firmwareFile returns the persistent firmware file to snapshot for this guest
// and the basename it takes inside a snapshot payload directory. VZ guests
// (macOS/Linux) use nvram.bin; Windows (QEMU) guests persist UEFI variables in
// efi_vars.fd. Detection is by file existence, NOT by reading config.json:
// config.json holds the run process's fcntl PID lock, and opening it from that
// same process would release the lock (POSIX fcntl semantics), making weave
// believe a running VM had stopped.
func (d *VMDirectory) firmwareFile() (path, name string) {
	if fsutil.Exists(d.NvramURL()) {
		return d.NvramURL(), "nvram.bin"
	}
	return d.EFIVarsURL(), "efi_vars.fd"
}

// snapshotRequiredBytes is the worst-case space a snapshot needs: a full copy of
// the disk image plus firmware. APFS copy-on-write usually makes the clone
// near-free, but this is the safe upper bound used by the free-space guard.
func (d *VMDirectory) snapshotRequiredBytes() (int64, error) {
	total, err := fsutil.AllocatedSizeBytes(d.DiskURL())
	if err != nil {
		return 0, err
	}
	if fwPath, _ := d.firmwareFile(); fsutil.Exists(fwPath) {
		if fw, err := fsutil.AllocatedSizeBytes(fwPath); err == nil {
			total += fw
		}
	}
	return total, nil
}

func (d *VMDirectory) readSnapshotIndex() (snapshotIndex, error) {
	var idx snapshotIndex
	data, err := os.ReadFile(d.snapshotIndexURL())
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

func (d *VMDirectory) writeSnapshotIndex(idx snapshotIndex) error {
	if err := os.MkdirAll(d.SnapshotsDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(d.snapshotIndexURL(), data, 0o644)
}

func findSnapshot(idx snapshotIndex, ref string) (int, bool) {
	for i, s := range idx.Snapshots {
		if s.ID == ref || strings.EqualFold(s.Name, ref) {
			return i, true
		}
	}
	return -1, false
}

// ListSnapshots returns the VM's snapshots in creation order.
func (d *VMDirectory) ListSnapshots() ([]Snapshot, error) {
	idx, err := d.readSnapshotIndex()
	if err != nil {
		return nil, err
	}
	return idx.Snapshots, nil
}

// CreateSnapshot clones the VM's current disk and firmware into a new named
// snapshot, and — when opts.SaveState is set — also captures the live RAM/device
// state. The caller quiesces the disk first (the VM must be stopped, or paused
// via the run process) so the image is consistent. Enforces unique names, the
// MaxSnapshots cap, and a free-space guard.
func (d *VMDirectory) CreateSnapshot(opts SnapshotCreateOptions) (Snapshot, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return Snapshot{}, weaveerrors.ErrGeneric("a snapshot name is required")
	}

	idx, err := d.readSnapshotIndex()
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
	required, err := d.snapshotRequiredBytes()
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
	payload := d.snapshotPayloadDir(id)
	if err := os.MkdirAll(payload, 0o755); err != nil {
		return Snapshot{}, err
	}
	cleanup := func() { _ = os.RemoveAll(payload) }

	if err := fsutil.CloneFile(d.DiskURL(), filepath.Join(payload, "disk.img")); err != nil {
		cleanup()
		return Snapshot{}, err
	}
	if fwPath, fwName := d.firmwareFile(); fsutil.Exists(fwPath) {
		if err := fsutil.CloneFile(fwPath, filepath.Join(payload, fwName)); err != nil {
			cleanup()
			return Snapshot{}, err
		}
	}

	hasState := false
	if opts.SaveState != nil {
		if err := opts.SaveState(d.snapshotStatePath(id)); err != nil {
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
	if err := d.writeSnapshotIndex(idx); err != nil {
		cleanup()
		return Snapshot{}, err
	}
	return snap, nil
}

func (d *VMDirectory) snapshotStatePath(id string) string {
	return filepath.Join(d.snapshotPayloadDir(id), "state.vzvmsave")
}

// SnapshotByRef returns the snapshot matching ref (name or id).
func (d *VMDirectory) SnapshotByRef(ref string) (Snapshot, bool, error) {
	idx, err := d.readSnapshotIndex()
	if err != nil {
		return Snapshot{}, false, err
	}
	if i, ok := findSnapshot(idx, ref); ok {
		return idx.Snapshots[i], true, nil
	}
	return Snapshot{}, false, nil
}

// RevertSnapshot restores the VM's disk and firmware from the named snapshot
// (matched by name or id), and stages its RAM state (if any) at the VM's state
// path so the next start resumes the exact moment rather than rebooting. The VM
// must be stopped — the live disk is replaced. Returns whether RAM state was
// staged.
func (d *VMDirectory) RevertSnapshot(ref string) (restoredState bool, err error) {
	idx, err := d.readSnapshotIndex()
	if err != nil {
		return false, err
	}
	i, ok := findSnapshot(idx, ref)
	if !ok {
		return false, weaveerrors.ErrGeneric("no snapshot named %q", ref)
	}
	snap := idx.Snapshots[i]
	payload := d.snapshotPayloadDir(snap.ID)

	diskSrc := filepath.Join(payload, "disk.img")
	if !fsutil.Exists(diskSrc) {
		return false, weaveerrors.ErrGeneric("snapshot %q is missing its disk image", snap.Name)
	}
	if err := fsutil.CloneFile(diskSrc, d.DiskURL()); err != nil {
		return false, err
	}
	if fwPath, fwName := d.firmwareFile(); fsutil.Exists(filepath.Join(payload, fwName)) {
		if err := fsutil.CloneFile(filepath.Join(payload, fwName), fwPath); err != nil {
			return false, err
		}
	}

	// Stage (or clear) the RAM state so the next start resumes from it (or boots
	// fresh for a disk-only snapshot).
	stateSrc := d.snapshotStatePath(snap.ID)
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

// DeleteSnapshot removes the named snapshot (matched by name or id) and its
// payload. Safe at any VM state — it never touches the live disk.
func (d *VMDirectory) DeleteSnapshot(ref string) error {
	idx, err := d.readSnapshotIndex()
	if err != nil {
		return err
	}
	i, ok := findSnapshot(idx, ref)
	if !ok {
		return weaveerrors.ErrGeneric("no snapshot named %q", ref)
	}
	snap := idx.Snapshots[i]
	if err := os.RemoveAll(d.snapshotPayloadDir(snap.ID)); err != nil {
		return err
	}
	idx.Snapshots = append(idx.Snapshots[:i], idx.Snapshots[i+1:]...)
	return d.writeSnapshotIndex(idx)
}
