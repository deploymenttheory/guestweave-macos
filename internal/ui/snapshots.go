// Snapshot menu actions for the run window: take a disk snapshot of the live VM
// (pause → clone → resume, in-process), and revert to or delete an existing one.
// Disk snapshots impose no constraints on the running configuration and need no
// particular macOS version, unlike the VZ save/restore "Suspend" action.
//go:build darwin

package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	appkit "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/appkit"
	corefoundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/corefoundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
)

// nsAlertFirstButtonReturn is the modal response of an NSAlert's first button;
// subsequent buttons return consecutive higher values.
const nsAlertFirstButtonReturn = 1000

// activeVMDirectory wraps the running VM's bundle directory for snapshot calls.
func activeVMDirectory() *vmdirectory.VMDirectory {
	return vmdirectory.NewVMDirectory(activeVMDir)
}

// takeSnapshotFromMenu is the Control ▸ Snapshots ▸ Take Snapshot action. It
// prompts for a name and description (on the main thread), then pauses, clones,
// and resumes the VM on a background goroutine — VZ control calls block on the
// main-queue completion, so they must not run on the main thread.
func takeSnapshotFromMenu() {
	if activeVM == nil {
		showInfo("Snapshot unavailable", "The VM is not running.")
		return
	}
	name, ok := promptText("Take Snapshot", "Name this snapshot:", "")
	if !ok {
		return
	}
	if name = strings.TrimSpace(name); name == "" {
		showError("Snapshot", "A snapshot name is required.")
		return
	}
	description, _ := promptText("Snapshot Description", "Optional description for "+name+":", "")

	vmDir := activeVMDirectory()
	go func() {
		snapshot, err := activeVM.CreateSnapshotPaused(vmDir, name, strings.TrimSpace(description))
		// The idiomatic AppKit bindings auto-dispatch onto the main thread
		// (purego.Main), so these alerts are safe to call straight from this
		// goroutine — no manual main-thread hop needed.
		if err != nil {
			showError("Snapshot failed", err.Error())
			return
		}
		showInfo("Snapshot created", fmt.Sprintf("Created snapshot %q.", snapshot.Name))
	}()
}

// revertSnapshotFromMenu lets the user pick a snapshot to revert to. Reverting
// replaces the live disk, so it can't be done in-place: the VM is gracefully
// stopped and a detached process reverts the disk then re-runs with the same
// options (mirrors restartVM).
func revertSnapshotFromMenu() {
	snapshots, err := activeVMDirectory().ListSnapshots()
	if err != nil {
		showError("Snapshots", err.Error())
		return
	}
	if len(snapshots) == 0 {
		showInfo("No snapshots", "This VM has no snapshots yet. Use Take Snapshot to create one.")
		return
	}
	names := snapshotNames(snapshots)
	idx, ok := chooseFromList("Revert to Snapshot",
		"Choose a snapshot. The VM will power off and restart from that disk state — changes since then are lost.", names)
	if !ok {
		return
	}
	if !confirm("Revert VM?",
		fmt.Sprintf("Revert to snapshot %q? The VM restarts from that point and any changes since are lost.", names[idx])) {
		return
	}
	relaunchWithRevert(names[idx])
}

// deleteSnapshotFromMenu lets the user pick a snapshot to delete. Deleting never
// touches the live disk, so it is safe while the VM runs.
func deleteSnapshotFromMenu() {
	vmDir := activeVMDirectory()
	snapshots, err := vmDir.ListSnapshots()
	if err != nil {
		showError("Snapshots", err.Error())
		return
	}
	if len(snapshots) == 0 {
		showInfo("No snapshots", "This VM has no snapshots to delete.")
		return
	}
	names := snapshotNames(snapshots)
	idx, ok := chooseFromList("Delete Snapshot", "Choose a snapshot to delete.", names)
	if !ok {
		return
	}
	if !confirm("Delete snapshot?", fmt.Sprintf("Permanently delete snapshot %q?", names[idx])) {
		return
	}
	if err := vmDir.DeleteSnapshot(names[idx]); err != nil {
		showError("Delete failed", err.Error())
		return
	}
	showInfo("Snapshot deleted", fmt.Sprintf("Deleted snapshot %q.", names[idx]))
}

func snapshotNames(snapshots []vmdirectory.Snapshot) []string {
	names := make([]string, len(snapshots))
	for i, s := range snapshots {
		names[i] = s.Name
	}
	return names
}

// relaunchWithRevert gracefully stops this run and starts a detached process
// that reverts the disk to ref (now that the VM is stopped) then re-runs with
// the original options.
func relaunchWithRevert(ref string) {
	exe, err := os.Executable()
	if err != nil {
		showError("Revert failed", err.Error())
		return
	}
	runCmd := shellJoin(append([]string{exe}, os.Args[1:]...))
	revertCmd := shellJoin([]string{exe, "snapshot", "revert", activeVM.Name, ref})
	// The child sleeps so this process releases the VM lock first, reverts, then
	// re-runs only if the revert succeeded.
	script := "sleep 3; " + revertCmd + " && exec " + runCmd
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survive this process exiting
	if err := cmd.Start(); err != nil {
		showError("Revert failed", err.Error())
		return
	}
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)
}

// promptText shows a modal alert with a single-line text field and returns the
// entered text and whether OK was pressed. Must run on the main thread.
func promptText(title, message, defaultValue string) (string, bool) {
	alert := appkit.NewAlert()
	alert.WithMessageText(title)
	alert.WithInformativeText(message)
	alert.AddButtonWithTitle("OK")
	alert.AddButtonWithTitle("Cancel")

	field := appkit.TextFieldFromID(objcutil.AllocClass("NSTextField").Send(purego.RegisterName("init")))
	field.WithFrame(corefoundation.CGRect{Size: corefoundation.CGSize{Width: 280, Height: 24}})
	field.WithEditable(true)
	field.WithBezeled(true)
	field.WithDrawsBackground(true)
	if defaultValue != "" {
		field.WithStringValue(defaultValue)
	}
	alert.WithAccessoryView(field)

	if alert.RunModal() != nsAlertFirstButtonReturn {
		return "", false
	}
	return appkit.ControlFromID(obj.ID(field)).StringValue(), true
}

// chooseFromList shows a modal alert with one button per option plus Cancel, and
// returns the chosen index. Must run on the main thread.
func chooseFromList(title, message string, options []string) (int, bool) {
	alert := appkit.NewAlert()
	alert.WithMessageText(title)
	alert.WithInformativeText(message)
	for _, option := range options {
		alert.AddButtonWithTitle(option)
	}
	alert.AddButtonWithTitle("Cancel")

	idx := alert.RunModal() - nsAlertFirstButtonReturn
	if idx < 0 || idx >= len(options) {
		return -1, false
	}
	return idx, true
}
