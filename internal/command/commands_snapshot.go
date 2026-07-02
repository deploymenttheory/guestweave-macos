// Disk snapshot commands: create / list / revert / delete named, point-in-time
// snapshots of a VM's disk. Create works on a running VM (the run process
// pauses, clones, and resumes it via the snapshot socket) or a stopped one
// (cloned directly). Revert requires the VM to be stopped.
//
// The exported Create/List/Revert/Delete functions hold the shared logic so the
// CLI command structs and the REST API call the same code path.
//go:build darwin

package command

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/vm/snapshot"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
	"github.com/deploymenttheory/guestweave/internal/vmstorage"
)

func openLocalVMDir(name string) (*layout.VMDirectory, error) {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return nil, err
	}
	return storage.Open(name)
}

// CreateSnapshot creates a named disk snapshot of vmName. Trylock atomically
// distinguishes a stopped VM (the lock is acquired and the disk cloned directly)
// from a running one (the lock is held by the run process, which performs the
// pause/clone/resume over the snapshot socket).
func CreateSnapshot(vmName, name, description string) (snapshot.Snapshot, error) {
	vmDir, err := openLocalVMDir(vmName)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	lock, err := vmDir.Lock()
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	defer lock.Close()
	acquired, err := lock.Trylock()
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	if acquired {
		// Stopped VM: no RAM to capture, so this is a disk-only snapshot.
		defer func() { _ = lock.Unlock() }()
		return snapshot.Create(vmDir, snapshot.CreateOptions{Name: name, Description: description})
	}
	return snapshot.RequestCreateOverSocket(vmDir, name, description)
}

// ListSnapshots returns vmName's disk snapshots.
func ListSnapshots(vmName string) ([]snapshot.Snapshot, error) {
	vmDir, err := openLocalVMDir(vmName)
	if err != nil {
		return nil, err
	}
	return snapshot.List(vmDir)
}

// RevertSnapshot restores vmName's disk from a named snapshot. The VM must be
// stopped, since the live disk is replaced.
func RevertSnapshot(vmName, ref string) error {
	vmDir, err := openLocalVMDir(vmName)
	if err != nil {
		return err
	}
	lock, err := vmDir.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	acquired, err := lock.Trylock()
	if err != nil {
		return err
	}
	if acquired {
		// Stopped VM: revert the files directly.
		defer func() { _ = lock.Unlock() }()
		_, err = snapshot.Revert(vmDir, ref)
		return err
	}

	// Running VM: ask the run process to revert in place (rebuild + re-point the
	// window). Fall back to "stop first" if it can't be reverted in-process.
	if err := snapshot.RequestRevertOverSocket(vmDir, ref); err != nil {
		if err == snapshot.ErrSocketUnavailable {
			return weaveerrors.ErrGeneric("the VM is running; stop it before reverting a snapshot")
		}
		return err
	}
	return nil
}

// DeleteSnapshot deletes a named snapshot. Safe at any VM state.
func DeleteSnapshot(vmName, ref string) error {
	vmDir, err := openLocalVMDir(vmName)
	if err != nil {
		return err
	}
	return snapshot.Delete(vmDir, ref)
}

// SnapshotCreateCommand creates a named disk snapshot of a VM.
type SnapshotCreateCommand struct {
	VM          string
	Name        string
	Description string
}

func (c *SnapshotCreateCommand) Run(ctx context.Context) error {
	snap, err := CreateSnapshot(c.VM, c.Name, c.Description)
	if err != nil {
		return err
	}
	fmt.Printf("Created snapshot %q\n", snap.Name)
	return nil
}

// SnapshotListCommand lists a VM's disk snapshots.
type SnapshotListCommand struct {
	VM string
}

func (c *SnapshotListCommand) Run(ctx context.Context) error {
	snapshots, err := ListSnapshots(c.VM)
	if err != nil {
		return err
	}
	if len(snapshots) == 0 {
		fmt.Printf("No snapshots for %q.\n", c.VM)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCREATED\tDESCRIPTION")
	for _, s := range snapshots {
		description := s.Description
		if description == "" {
			description = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.CreatedAt.Local().Format(time.RFC3339), description)
	}
	return w.Flush()
}

// SnapshotRevertCommand restores a VM's disk from a named snapshot.
type SnapshotRevertCommand struct {
	VM  string
	Ref string
}

func (c *SnapshotRevertCommand) Run(ctx context.Context) error {
	if err := RevertSnapshot(c.VM, c.Ref); err != nil {
		return err
	}
	fmt.Printf("Reverted %q to snapshot %q\n", c.VM, c.Ref)
	return nil
}

// SnapshotDeleteCommand deletes a named snapshot.
type SnapshotDeleteCommand struct {
	VM  string
	Ref string
}

func (c *SnapshotDeleteCommand) Run(ctx context.Context) error {
	if err := DeleteSnapshot(c.VM, c.Ref); err != nil {
		return err
	}
	fmt.Printf("Deleted snapshot %q\n", c.Ref)
	return nil
}
