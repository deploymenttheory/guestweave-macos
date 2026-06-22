// Menu action handlers for the run window. These surface internal/command
// capabilities (ssh, exec, ip, logs, …) in the GUI, either in-process (against
// the live activeVM) or by launching the guestweave CLI in Terminal. All AppKit
// use is idiomatic; internal/command stays headless.
//go:build darwin

package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/macaddress"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/screenviewer"

	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	appkit "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/appkit"
	corefoundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/corefoundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
)

// vncURLPattern mirrors unattended.VNCURLPattern (vnc://:password@host:port);
// duplicated here to keep the ui package free of a heavier dependency.
var vncURLPattern = regexp.MustCompile(`vnc://:([^@]+)@([\d.]+):(\d+)`)

// nsPasteboardTypeString is the UTI for plain UTF-8 text on NSPasteboard
// (NSPasteboardTypeString).
const nsPasteboardTypeString = "public.utf8-plain-text"

// Window-scoped state shared with the ObjC menu-target callbacks (which cannot
// carry Go closure state). activeVMDir and activeView are set in Run() on the
// main thread before the run loop starts; vncURL and clipboardStatus arrive
// asynchronously from the run command's driveVM goroutine, so they are atomics.
var (
	activeVMDir string
	activeView  *virtualization.VirtualMachineView

	vncURLHolder    atomic.Value // string
	clipboardStatus atomic.Value // string

	screenShareMu     sync.Mutex
	screenShareCancel context.CancelFunc
)

// SetVNCURL records the running VM's VNC URL once the server is up, enabling the
// Connect ▸ Open VNC Viewer and View ▸ Toggle Screen Share menu items.
func SetVNCURL(url string) { vncURLHolder.Store(url) }

// SetClipboardStatus records the resolved clipboard direction for the
// Control ▸ Clipboard Status item (empty = clipboard sync disabled).
func SetClipboardStatus(status string) { clipboardStatus.Store(status) }

func loadVNCURL() string        { s, _ := vncURLHolder.Load().(string); return s }
func loadClipboardStatus() string { s, _ := clipboardStatus.Load().(string); return s }

// ── Connect ─────────────────────────────────────────────────────────────────

// connectSSH opens Terminal with `guestweave ssh <name>` (interactive shell).
func connectSSH() { launchInTerminal("ssh", activeVM.Name) }

// openGuestShell opens Terminal with `guestweave exec -it <name> /bin/sh`
// (works over the vsock guest agent regardless of network isolation).
func openGuestShell() { launchInTerminal("exec", "-it", activeVM.Name, "/bin/sh") }

// openVNCViewer opens the running VM's VNC URL with the default handler.
func openVNCViewer() {
	url := loadVNCURL()
	if url == "" {
		showInfo("VNC not available", "Re-run the VM with --vnc to enable a VNC server.")
		return
	}
	OpenURL(url)
}

// copyIPAddress resolves the guest IP (no wait — cache only, to keep the UI
// responsive) and copies it to the pasteboard.
func copyIPAddress() {
	mac, ok := macaddress.NewMACAddress(activeVM.Config.MACAddress.String())
	if !ok {
		showInfo("IP unavailable", "The VM has no resolvable MAC address.")
		return
	}
	addr, found, err := macaddress.ResolveIP(
		context.Background(), mac, macaddress.IPResolutionStrategyDHCP, 0,
		filepath.Join(activeVMDir, "control.sock"))
	if err != nil || !found {
		showInfo("IP unavailable", "Could not resolve the guest IP yet (the guest may still be booting, or its network is isolated).")
		return
	}
	ip := addr.String()
	pb := appkit.GeneralPasteboard()
	pb.ClearContents()
	pb.SetStringForType(ip, objcutil.NSStr(nsPasteboardTypeString))
	showInfo("Copied", "Guest IP "+ip+" copied to the clipboard.")
}

// launchInTerminal writes a temporary executable .command that execs the
// guestweave CLI with args and opens it in Terminal. Using a .command file with
// `open -a Terminal` avoids the AppleScript automation permission prompt.
func launchInTerminal(args ...string) {
	exe, err := os.Executable()
	if err != nil {
		showError("guestweave not found", err.Error())
		return
	}
	script := "#!/bin/sh\nexec " + shellJoin(append([]string{exe}, args...)) + "\n"
	f, err := os.CreateTemp("", "guestweave-*.command")
	if err != nil {
		showError("Cannot create launcher", err.Error())
		return
	}
	name := f.Name()
	_, _ = f.WriteString(script)
	_ = f.Close()
	_ = os.Chmod(name, 0o700)
	if err := exec.Command("open", "-a", "Terminal", name).Run(); err != nil {
		showError("Cannot open Terminal", err.Error())
	}
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// ── File ────────────────────────────────────────────────────────────────────

// revealInFinder selects the VM bundle in a Finder window.
func revealInFinder() {
	if activeVMDir == "" {
		return
	}
	appkit.SharedWorkspace().SelectFileInFileViewerRootedAtPath(activeVMDir, "")
}

// showLogs opens the error log in the default viewer (Console).
func showLogs() {
	dir := logging.LogsDir()
	if dir == "" {
		showInfo("No logs", "The log directory is unavailable.")
		return
	}
	appkit.SharedWorkspace().OpenFile(filepath.Join(dir, logging.LogFileErrorName))
}

// clearLogs deletes all log files (info, error and rotated copies) after
// confirmation — the GUI counterpart of `weave logs clear`.
func clearLogs() {
	if !confirm("Clear Logs?", "This permanently deletes all guestweave log files (info, error, and rotated copies).") {
		return
	}
	if err := logging.Clear(); err != nil {
		showError("Clear Logs failed", err.Error())
		return
	}
	showInfo("Logs cleared", "All guestweave log files have been removed.")
}

// ── View ────────────────────────────────────────────────────────────────────

// takeScreenshot renders the VM view to a PNG on the Desktop. Note: a
// Metal/IOSurface-backed guest framebuffer may not be captured by AppKit's
// cacheDisplay path on all guests; VNC remains the fully reliable source.
func takeScreenshot() {
	if activeView == nil {
		return
	}
	rect := corefoundation.CGRect{Size: corefoundation.CGSize{
		Width:  float64(activeVM.Config.Display.Width),
		Height: float64(activeVM.Config.Display.Height),
	}}
	view := appkit.ViewFromID(obj.ID(activeView))
	rep := view.BitmapImageRepForCachingDisplayInRect(rect)
	if rep == nil {
		showError("Screenshot failed", "Could not allocate a bitmap for the view.")
		return
	}
	view.CacheDisplayInRectToBitmapImageRep(rect, rep)
	png := rep.RepresentationUsingTypeProperties(appkit.BitmapImageFileTypePNG, nil)
	if png == nil {
		showError("Screenshot failed", "PNG encoding failed.")
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		showError("Screenshot failed", err.Error())
		return
	}
	path := filepath.Join(home, "Desktop",
		fmt.Sprintf("guestweave-%s-%s.png", activeVM.Name, time.Now().Format("20060102-150405")))
	if err := os.WriteFile(path, obj.Bytes(png), 0o644); err != nil {
		showError("Screenshot failed", err.Error())
		return
	}
	showInfo("Screenshot saved", path)
}

// toggleScreenShare starts or stops a view-only browser screen share backed by
// the VNC framebuffer.
func toggleScreenShare() {
	screenShareMu.Lock()
	defer screenShareMu.Unlock()

	if screenShareCancel != nil {
		screenShareCancel()
		screenShareCancel = nil
		showInfo("Screen Share", "View-only screen share stopped.")
		return
	}

	url := loadVNCURL()
	if url == "" {
		showInfo("Screen Share", "Requires a VNC server — re-run the VM with --vnc.")
		return
	}
	match := vncURLPattern.FindStringSubmatch(url)
	if match == nil {
		showError("Screen Share", "Could not parse the VNC URL.")
		return
	}
	port, err := strconv.Atoi(match[3])
	if err != nil {
		showError("Screen Share", "Invalid VNC port.")
		return
	}
	server, err := screenviewer.NewScreenServer()
	if err != nil {
		showError("Screen Share", err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	screenShareCancel = cancel
	go screenviewer.StreamVNCToViewer(ctx, match[2], port, match[1], server)
	screenviewer.OpenInBrowser(server.URL())
	showInfo("Screen Share", "View-only screen available at "+server.URL())
}

// ── Weave / Control ───────────────────────────────────────────────────────────

// showVMInfo presents the VM's resolved configuration.
func showVMInfo() {
	c := activeVM.Config
	info := fmt.Sprintf(
		"Name: %s\nOS: %s\nCPU: %d cores\nMemory: %d MB\nDisplay: %s\nDirectory: %s",
		activeVM.Name, c.OS, c.CPUCount, c.MemorySize/1024/1024, c.Display.String(), activeVMDir)
	showInfo("VM Info", info)
}

// showClipboardStatus reports the resolved clipboard sync direction. (Toggling
// at runtime would require new hooks on the clipboard engine; this is read-only.)
func showClipboardStatus() {
	status := loadClipboardStatus()
	if status == "" {
		showInfo("Clipboard", "Clipboard sync is disabled for this VM.")
		return
	}
	showInfo("Clipboard", "Clipboard sync direction: "+status)
}

// forceStop terminates the run process immediately (SIGKILL), powering off the
// VM without a clean guest shutdown — the last resort for a hung guest.
func forceStop() {
	if !confirm("Force Stop?", "This powers off the VM immediately, without a clean guest shutdown. Unsaved guest data may be lost.") {
		return
	}
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGKILL)
}

// restartVM relaunches the VM with the same options. An in-process VZ stop/start
// is unsafe here: VZVirtualMachine control methods must run on the VM's
// main-thread queue, and a stop tears this run process down. So instead it
// gracefully stops the current run (SIGINT, like the Stop menu) and starts a
// detached `guestweave run` that reproduces this process's original invocation.
func restartVM() {
	if !confirm("Restart VM?", "The VM will be powered off and started again with the same options.") {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		showError("Restart failed", err.Error())
		return
	}
	// The child sleeps briefly so this process releases the VM lock first, then
	// re-runs with the original args (os.Args[1:] = the run subcommand + flags).
	relaunch := shellJoin(append([]string{exe}, os.Args[1:]...))
	cmd := exec.Command("/bin/sh", "-c", "sleep 3; exec "+relaunch)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survive this process exiting
	if err := cmd.Start(); err != nil {
		showError("Restart failed", err.Error())
		return
	}
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)
}

// ── Dialog helpers ────────────────────────────────────────────────────────────

func showInfo(title, message string)  { runAlert(title, message) }
func showError(title, message string) { runAlert(title, message) }

func runAlert(title, message string) {
	alert := appkit.NewAlert()
	alert.WithMessageText(title)
	alert.WithInformativeText(message)
	alert.RunModal()
}

// confirm shows an OK/Cancel alert, returning true for OK
// (NSAlertFirstButtonReturn == 1000).
func confirm(title, message string) bool {
	alert := appkit.NewAlert()
	alert.WithMessageText(title)
	alert.WithInformativeText(message)
	alert.AddButtonWithTitle("OK")
	alert.AddButtonWithTitle("Cancel")
	return alert.RunModal() == 1000
}
