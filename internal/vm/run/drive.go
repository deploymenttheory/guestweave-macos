// driveVM: the run loop — restore/start the VM, bring up one-time services,
// wait, and support in-process snapshot revert (TriggerRevert + the snapshot
// socket LiveHandler).
//go:build darwin

package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego/objcerrors"
	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	"github.com/deploymenttheory/guestweave/internal/clipboard"
	"github.com/deploymenttheory/guestweave/internal/controlsocket"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/fsutil"
	"github.com/deploymenttheory/guestweave/internal/macaddress"
	"github.com/deploymenttheory/guestweave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
	"github.com/deploymenttheory/guestweave/internal/screenviewer"
	"github.com/deploymenttheory/guestweave/internal/telemetry"
	"github.com/deploymenttheory/guestweave/internal/ui"
	"github.com/deploymenttheory/guestweave/internal/unattended"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
	"github.com/deploymenttheory/guestweave/internal/vm/snapshot"
	vmstorage "github.com/deploymenttheory/guestweave/internal/vm/storage"
	weavevnc "github.com/deploymenttheory/guestweave/internal/vnc"
)

// TriggerRevert requests an in-process revert to ref: it records the target
// snapshot and cancels the current VM's context so driveVM's run loop
// rebuilds the VM in place (same process and window) instead of exiting. It
// returns false when in-process revert isn't available (e.g. a VNC run, or no
// live VM yet), so the caller can fall back to the relaunch path.
func (c *Session) TriggerRevert(ref string) bool {
	c.revertMu.Lock()
	defer c.revertMu.Unlock()
	if !c.inProcessRevertReady || c.currentVMCancel == nil {
		return false
	}
	c.pendingRevertRef = ref
	c.currentVMCancel() // unblocks vm.Run; driveVM sees the pending ref and rebuilds
	return true
}

// liveSnapshotHandler adapts the Session to the snapshot socket's LiveHandler:
// only the run process owns the VZ handle needed to pause → clone → resume or
// revert in place.
type liveSnapshotHandler struct {
	s   *Session
	dir *layout.VMDirectory
}

func (h liveSnapshotHandler) CreateLive(name, description string) (snapshot.Snapshot, error) {
	if h.s.vm == nil {
		return snapshot.Snapshot{}, weaveerrors.ErrGeneric("the VM is not running")
	}
	return h.s.vm.CreateSnapshotPaused(h.dir, name, description)
}

func (h liveSnapshotHandler) RevertInProcess(ref string) bool {
	return h.s.TriggerRevert(ref)
}

// driveVM ports the inner Task of Run.runOnMainThread(): restores a
// snapshot if present, starts the VM, brings up VNC and the control socket,
// then waits for the VM to finish. It loops to support in-process snapshot
// revert, rebuilding the VM and re-pointing the window's view without exiting.
func (c *Session) driveVM(
	ctx context.Context,
	localStorage *vmstorage.VMStorageLocal,
	vmDir *layout.VMDirectory,
	vncImpl weavevnc.VNC,
	vmConfig *vmconfig.VMConfig,
) {
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		telemetry.OTelShared().Flush()
		os.Exit(1)
	}

	// servicesStarted gates the one-time, process-lifetime services (clipboard,
	// VNC, control socket) so they aren't restarted when the VM is rebuilt for an
	// in-process snapshot revert.
	servicesStarted := false

	for {
		// Per-iteration cancellation: an in-process revert cancels vmCtx to
		// unblock vm.Run and rebuild the VM, without tearing down the process.
		vmCtx, cancelVM := context.WithCancel(ctx)
		c.revertMu.Lock()
		c.currentVMCancel = cancelVM
		c.revertMu.Unlock()

		// Restore from a staged state file: the initial resume of a suspended VM,
		// or the RAM state staged by a full-state snapshot revert.
		resume := false
		if weaveplatform.MacOSAtLeast(14) && fsutil.Exists(vmDir.StateURL()) {
			fmt.Println("restoring VM state from a snapshot...")
			if err := c.vm.RestoreMachineStateFrom(vmDir.StateURL()); err != nil {
				cancelVM()
				fail(err)
				return
			}
			if err := os.RemoveAll(vmDir.StateURL()); err != nil {
				cancelVM()
				fail(err)
				return
			}
			resume = true
			fmt.Println("resuming VM...")
		}

		if err := c.vm.Start(c.Recovery, resume); err != nil {
			cancelVM()
			var objcErr *objcerrors.ObjCError
			if errors.As(err, &objcErr) && objcErr.Domain == "VZErrorDomain" &&
				objcErr.Code == int64(idvirt.ErrorVirtualMachineLimitExceeded) {
				hint := ""
				if entries, listErr := localStorage.List(); listErr == nil {
					var runningVMs []string
					for _, entry := range entries {
						if running, err := entry.VMDir.Running(); err == nil && running {
							runningVMs = append(runningVMs, entry.Name)
						}
					}
					if len(runningVMs) > 0 {
						hint = " (other running VMs: " + strings.Join(runningVMs, ", ") + ")"
					}
				}
				fail(weaveerrors.ErrVirtualMachineLimitExceeded(hint))
				return
			}
			fail(err)
			return
		}

		if !servicesStarted {
			servicesStarted = true

			// Enterprise clipboard engine (policy-driven, via the guest agent).
			// Resolved in RunMainThread; when active it owns the clipboard and the
			// SPICE agent clipboard is disabled (see VMOptions.ClipboardPolicyEnabled).
			if c.clipboardRun {
				// Use the already-loaded config's MAC rather than vmDir.MACAddress(),
				// which would reopen config.json and drop this process's fcntl PID
				// lock (making the VM misreport as stopped).
				if vmMAC, ok := macaddress.NewMACAddress(vmConfig.MACAddress.String()); ok {
					engine := clipboard.NewEngine(c.clipboardPolicy, c.Name, vmDir, vmMAC,
						c.ClipboardUser, c.ClipboardPassword, c.guestGOOS, c.guestGOARCH)
					// The engine reports a live health snapshot each sync cycle to
					// the Control ▸ Clipboard Status panel.
					engine.SetReporter(ui.SetClipboardHealth)
					// The resident guest agent is reached over the dedicated virtio
					// serial channel built in buildVMInstance; hand the engine its
					// host ends. SSH (creds below) is used only to install the agent.
					if c.clipSerialHostR != nil && c.clipSerialHostW != nil {
						engine.SetSerialChannel(c.clipSerialHostR, c.clipSerialHostW)
					}
					c.clipboardEngine = engine
					go engine.Run(ctx)
					// Host control socket for live `weave clipboard set` updates.
					go c.serveClipboardControl(ctx, vmDir, engine)
				}
			}

			if vncImpl != nil {
				vncURL, err := vncImpl.WaitForURL(ctx, c.primaryBridged)
				if err != nil {
					cancelVM()
					fail(err)
					return
				}

				// Surface the URL to the run window's Connect ▸ Open VNC Viewer and
				// View ▸ Toggle Screen Share menu items.
				ui.SetVNCURL(vncURL)

				// Record the VNC endpoint so other processes (the MCP screen tools)
				// can connect to drive or view this VM by name; clear it on exit.
				endpointPath := vmDir.VNCEndpointPath()
				_ = os.WriteFile(endpointPath, []byte(vncURL), 0o600)
				defer os.Remove(endpointPath)

				_, onCI := objcutil.EnvironmentValue("CI")
				if c.NoGraphics || onCI || c.ShowScreen {
					fmt.Printf("VNC server is running at %s\n", vncURL)
				} else {
					fmt.Printf("Opening %s...\n", vncURL)
					ui.OpenURL(vncURL)
				}

				// View-only screen viewer: a dedicated VNC client continuously
				// captures the screen and serves it as MJPEG to a browser, with no
				// path for the operator to send input into the guest.
				if c.ShowScreen {
					if match := unattended.VNCURLPattern.FindStringSubmatch(vncURL); match != nil {
						if viewerPort, convErr := strconv.Atoi(match[3]); convErr == nil {
							if server, srvErr := screenviewer.NewScreenServer(); srvErr == nil {
								go screenviewer.StreamVNCToViewer(
									ctx,
									match[2],
									viewerPort,
									match[1],
									server,
								)
								fmt.Printf(
									"View-only screen: open %s in a browser to watch (no input reaches the VM).\n",
									server.URL(),
								)
								screenviewer.OpenInBrowser(server.URL())
							}
						}
					}
				}
			}

			if weaveplatform.MacOSAtLeast(14) {
				go func() {
					controlSocket := controlsocket.NewControlSocket(vmDir.ControlSocketURL())
					_ = controlSocket.Run(ctx)
				}()
			}
		}

		if err := c.vm.Run(vmCtx); err != nil {
			cancelVM()
			fail(err)
			return
		}

		// vm.Run returned: the guest stopped, the process is shutting down, or an
		// in-process revert was requested. Claim any pending revert atomically.
		c.revertMu.Lock()
		ref := c.pendingRevertRef
		c.pendingRevertRef = ""
		c.currentVMCancel = nil
		c.revertMu.Unlock()
		cancelVM()

		if ref == "" {
			break // genuine stop / process shutdown
		}

		// In-process revert: the VM is stopped. Restore the snapshot's disk and
		// firmware (staging its RAM state, if any), rebuild the VM, and re-point
		// the window's view — all without exiting the process.
		fmt.Printf("reverting to snapshot %q...\n", ref)
		if _, err := snapshot.Revert(vmDir, ref); err != nil {
			fmt.Fprintln(os.Stderr, weaveerrors.ErrGeneric("revert failed: %v", err))
			break
		}
		// Rebuild from the already-loaded vmConfig so config.json is never
		// reopened in this process — reopening it would drop the fcntl PID lock
		// (POSIX semantics), making the VM report as stopped and letting a second
		// run start on it.
		newVM, err := c.buildVMInstance(vmDir, vmConfig)
		if err != nil {
			fail(err)
			return
		}
		c.vm = newVM
		controlsocket.SetConnector(c.vm)
		ui.SwapVM(c.vm)
	}

	if vncImpl != nil {
		if err := vncImpl.Stop(); err != nil {
			fail(err)
			return
		}
	}

	telemetry.OTelShared().Flush()
	os.Exit(0)
}
