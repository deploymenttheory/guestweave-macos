// Package vmrun owns the run workflow (a port of tart's Commands/Run.swift):
// it boots a VM, optionally with a UI window, VNC, additional disks,
// directory shares and custom networking, and serves the sockets other weave
// commands use to talk to the running VM. The CLI layer parses flags into an
// Options and hands the process's main thread to Run.
//go:build darwin

package vmrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego/objcerrors"
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	"github.com/deploymenttheory/guestweave/internal/clipboard"
	"github.com/deploymenttheory/guestweave/internal/clipboardctl"
	"github.com/deploymenttheory/guestweave/internal/clipboardpolicy"
	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	"github.com/deploymenttheory/guestweave/internal/controlsocket"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/fetcher"
	"github.com/deploymenttheory/guestweave/internal/fsutil"
	weavelock "github.com/deploymenttheory/guestweave/internal/lock"
	"github.com/deploymenttheory/guestweave/internal/logging"
	"github.com/deploymenttheory/guestweave/internal/macaddress"
	weavenetwork "github.com/deploymenttheory/guestweave/internal/network"
	"github.com/deploymenttheory/guestweave/internal/objcutil"
	"github.com/deploymenttheory/guestweave/internal/oci"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
	"github.com/deploymenttheory/guestweave/internal/screenviewer"
	"github.com/deploymenttheory/guestweave/internal/telemetry"
	"github.com/deploymenttheory/guestweave/internal/terminal"
	"github.com/deploymenttheory/guestweave/internal/ui"
	"github.com/deploymenttheory/guestweave/internal/unattended"
	weavevm "github.com/deploymenttheory/guestweave/internal/vm"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"
	"github.com/deploymenttheory/guestweave/internal/vm/snapshot"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
	"github.com/deploymenttheory/guestweave/internal/vmstorage"
	weavevnc "github.com/deploymenttheory/guestweave/internal/vnc"
	"github.com/deploymenttheory/guestweave/internal/winimage"
)

// parseDiskImageSynchronizationMode ports the VZDiskImageSynchronizationMode
// init(_ description:) extension.
func parseDiskImageSynchronizationMode(
	description string,
) (idvirt.DiskImageSynchronizationMode, error) {
	switch description {
	case "none":
		return idvirt.DiskImageSynchronizationModeNone, nil
	case "fsync":
		return idvirt.DiskImageSynchronizationModeFsync, nil
	case "full", "":
		return idvirt.DiskImageSynchronizationModeFull, nil
	default:
		return 0, weaveerrors.ErrVMConfigurationError(
			"unsupported disk image synchronization mode: %q",
			description,
		)
	}
}

// parseDiskSynchronizationMode ports the VZDiskSynchronizationMode
// init(_ description:) extension.
func parseDiskSynchronizationMode(description string) (idvirt.DiskSynchronizationMode, error) {
	switch description {
	case "none":
		return idvirt.DiskSynchronizationModeNone, nil
	case "full", "":
		return idvirt.DiskSynchronizationModeFull, nil
	default:
		return 0, weaveerrors.ErrVMConfigurationError(
			"unsupported disk synchronization mode: %q",
			description,
		)
	}
}

// parseDiskImageCachingMode ports the VZDiskImageCachingMode
// init?(_ description:) extension; ok=false mirrors the nil return for "".
func parseDiskImageCachingMode(description string) (idvirt.DiskImageCachingMode, bool, error) {
	switch description {
	case "automatic":
		return idvirt.DiskImageCachingModeAutomatic, true, nil
	case "cached":
		return idvirt.DiskImageCachingModeCached, true, nil
	case "uncached":
		return idvirt.DiskImageCachingModeUncached, true, nil
	case "":
		return 0, false, nil
	default:
		return 0, false, weaveerrors.ErrVMConfigurationError(
			"unsupported disk image caching mode: %q",
			description,
		)
	}
}

// Options carries the run parameters resolved by the CLI layer. String
// fields hold the operator's flag values verbatim; the clipboard policy
// override arrives pre-translated (clipboardpolicy.Override) so this package
// stays independent of the CLI flag grammar.
type Options struct {
	Name              string
	NoGraphics        bool
	Serial            bool
	SerialPath        string
	Graphics          bool
	NoAudio           bool
	NoClipboard       bool
	Clipboard         bool
	ClipboardUser     string
	ClipboardPassword string
	// ClipboardOverride layers the CLI's enterprise clipboard-policy flags
	// over the per-VM config, the settings default and the built-in default.
	ClipboardOverride clipboardpolicy.Override

	Recovery          bool
	VNC               bool
	VNCExperimental   bool
	VNCPassword       string
	Disk              []string
	RosettaTag        string
	Dir               []string
	SharedDir         []string
	USBStorage        []string
	Nested            bool
	NetProfile        string
	NetDevice         []string
	NetBridged        []string
	NetSoftnet        bool
	NetSoftnetAllow   string
	NetSoftnetBlock   string
	NetSoftnetExpose  string
	NetHost           bool
	RootDiskOpts      string
	Suspendable       bool
	CaptureSystemKeys bool
	NoTrackpad        bool
	NoPointer         bool
	NoKeyboard        bool
	ShowScreen        bool // serve a view-only browser viewer of the VM screen
}

// Session is one run of a VM: the Options plus every piece of runtime state
// the workflow used to keep in RunCommand fields and package globals.
type Session struct {
	Options

	// vm ports tart's global `var vm: VM?` from Run.swift.
	vm *weavevm.VM

	// Resolved in runMainThread, consumed in driveVM.
	clipboardPolicy clipboardpolicy.Policy
	clipboardRun    bool
	clipboardEngine *clipboard.Engine // retained for live policy updates
	guestGOOS       string
	guestGOARCH     string

	// Host ends of the clipboard agent's virtio serial channel. Created once
	// (lazily, in buildVMInstance) and reused across in-process VM rebuilds so the
	// clipboard engine's connection survives a snapshot revert. The host writes
	// requests to clipSerialHostW and reads responses from clipSerialHostR; the
	// VM's serial-port attachment is wired to the other ends.
	clipSerialHostR     *os.File
	clipSerialHostW     *os.File
	clipSerialVMReadFD  int // framework reads this (host→guest); raw/blocking
	clipSerialVMWriteFD int // framework writes guest output here (guest→host)

	// primaryBridged records whether the resolved primary NIC is bridged, so
	// the VNC layer resolves the guest IP via ARP rather than DHCP. Set during
	// runMainThread.
	primaryBridged bool

	// In-process snapshot revert coordination (see TriggerRevert).
	revertMu             sync.Mutex
	currentVMCancel      context.CancelFunc
	pendingRevertRef     string
	inProcessRevertReady bool
}

// Run executes the run workflow on the calling goroutine, which must be the
// process's locked main thread. It returns only on pre-flight errors; once
// the AppKit loop is entered the process exits from within (os.Exit), as the
// workflow always has.
func Run(opts Options) error {
	s := &Session{Options: opts}
	return s.runMainThread()
}

// Validate ports Run.validate().
func (c *Options) Validate() error {
	if c.VNC && c.VNCExperimental {
		return weaveerrors.ErrGeneric("--vnc and --vnc-experimental are mutually exclusive")
	}

	// Automatically enable --net-softnet when any of its related options
	// are specified.
	if c.NetSoftnetAllow != "" || c.NetSoftnetBlock != "" || c.NetSoftnetExpose != "" {
		c.NetSoftnet = true
	}

	// Check that no more than one network option is specified.
	netFlags := 0
	if len(c.NetBridged) > 0 {
		netFlags++
	}
	if c.NetSoftnet {
		netFlags++
	}
	if c.NetHost {
		netFlags++
	}
	if netFlags > 1 {
		return weaveerrors.ErrGeneric(
			"--net-bridged, --net-softnet and --net-host are mutually exclusive",
		)
	}

	// The high-level --net-profile, the primitive --net-device list, and the
	// legacy --net-* flags are three mutually exclusive ways to select
	// networking.
	legacyNet := len(c.NetBridged) > 0 || c.NetSoftnet || c.NetHost
	surfaces := 0
	if c.NetProfile != "" {
		surfaces++
	}
	if len(c.NetDevice) > 0 {
		surfaces++
	}
	if legacyNet {
		surfaces++
	}
	if surfaces > 1 {
		return weaveerrors.ErrGeneric(
			"--net-profile, --net-device and the legacy --net-* flags are mutually exclusive",
		)
	}

	// Fail fast on a bad profile name or --net-device spec, before the heavy
	// VM boot, by resolving them up front.
	if c.NetProfile != "" {
		if _, err := weavenetwork.ExpandProfile(
			c.NetProfile,
			weavenetwork.ProfileOptions{},
		); err != nil {
			return err
		}
	}
	if len(c.NetDevice) > 0 {
		if _, err := weavenetwork.ParseNICDevices(c.NetDevice); err != nil {
			return err
		}
	}

	if c.Graphics && c.NoGraphics {
		return weaveerrors.ErrGeneric("--graphics and --no-graphics are mutually exclusive")
	}

	if (c.NoGraphics || c.VNC || c.VNCExperimental) && c.CaptureSystemKeys {
		return weaveerrors.ErrGeneric(
			"--captures-system-keys can only be used with the default VM view",
		)
	}

	if c.Nested {
		if !weaveplatform.MacOSAtLeast(15) {
			return weaveerrors.ErrGeneric(
				"Nested virtualization is supported on hosts starting with macOS 15 (Sequoia), and later.",
			)
		}
		if !idvirt.IsNestedVirtualizationSupported() {
			return weaveerrors.ErrGeneric(
				"Nested virtualization is available for Mac with the M3 chip, and later.",
			)
		}
	}

	localStorage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := localStorage.Open(c.Name)
	if err != nil {
		return err
	}
	state, err := vmDir.State()
	if err != nil {
		return err
	}
	if state == layout.VMDirectoryStateSuspended {
		c.Suspendable = true
	}

	if c.Suspendable {
		config, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
		if err != nil {
			return err
		}
		if _, ok := config.Platform.(vmconfig.PlatformSuspendable); !ok {
			return weaveerrors.ErrGeneric("Only macOS and Linux VMs can be suspended")
		}

		if c.NoTrackpad {
			return weaveerrors.ErrGeneric("--no-trackpad cannot be used with --suspendable")
		}
		if c.NoKeyboard {
			return weaveerrors.ErrGeneric("--no-keyboard cannot be used with --suspendable")
		}
		if c.NoPointer {
			return weaveerrors.ErrGeneric("--no-pointer cannot be used with --suspendable")
		}
	}

	if c.NoTrackpad {
		config, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
		if err != nil {
			return err
		}
		if config.OS != weaveplatform.OSDarwin {
			return weaveerrors.ErrGeneric("--no-trackpad can only be used with macOS VMs")
		}
	}

	// Inspect any attached ISO's real architecture (ISO9660 volume id) rather
	// than trusting its filename: weave guests are ARM64 only.
	for _, disk := range c.Disk {
		if !strings.EqualFold(filepath.Ext(disk), ".iso") {
			continue
		}
		if err := winimage.RequireARM64ISO(disk); err != nil {
			return weaveerrors.ErrGeneric("%s", err.Error())
		}
	}

	return nil
}

// runMainThread ports Run.runOnMainThread(); the caller must be on the
// process's main thread (it ends in NSApplication.run()).
func (c *Session) runMainThread() error {
	localStorage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := localStorage.Open(c.Name)
	if err != nil {
		return err
	}

	vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return err
	}

	// Windows guests run on the QEMU backend, which is a self-contained
	// subprocess path with none of the VZ/AppKit machinery below.
	if vmConfig.OS == weaveplatform.OSWindows {
		return c.runWindows(vmDir, vmConfig)
	}

	// Validate disk format support.
	if !vmConfig.DiskFormat.IsSupported() {
		return weaveerrors.ErrGeneric(
			"Disk format '%s' is not supported on this system.",
			vmConfig.DiskFormat,
		)
	}

	c.resolveClipboard(vmConfig)

	config, err := weaveconfig.NewConfig()
	if err != nil {
		return err
	}
	storageLock, err := weavelock.NewFileLock(config.WeaveHomeDir)
	if err != nil {
		return err
	}
	defer storageLock.Close()
	if err := storageLock.Lock(); err != nil {
		return err
	}

	// Check if there is a running VM with the same MAC address but a
	// different name.
	vmDirMAC, err := vmDir.MACAddress()
	if err != nil {
		return err
	}
	entries, err := localStorage.List()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		running, err := entry.VMDir.Running()
		if err != nil || !running {
			continue
		}
		mac, err := entry.VMDir.MACAddress()
		if err == nil && mac == vmDirMAC && entry.Name != vmDir.Name() {
			fmt.Println("There is already a running VM with the same MAC address!")
			fmt.Println("Resetting VM to assign a new MAC address...")
			if err := vmDir.RegenerateMACAddress(); err != nil {
				return err
			}
			break
		}
	}

	c.vm, err = c.buildVMInstance(vmDir, vmConfig)
	if err != nil {
		return err
	}
	// Publish the VM for control socket clients (the monolith's package
	// global; the controlsocket package now receives it explicitly).
	controlsocket.SetConnector(c.vm)

	var vncImpl weavevnc.VNC
	switch {
	case c.VNC:
		vncImpl = weavevnc.NewScreenSharingVNC(vmConfig)
	case c.VNCExperimental:
		vncImpl = weavevnc.NewFullFledgedVNC(c.vm, c.VNCPassword)
	}

	// Lock the VM. More specifically, lock "config.json", because we can't
	// lock directories with fcntl(2)-based locking and we'd better not
	// interfere with the VM's disk and NVRAM (they are opened directly by
	// the Virtualization.Framework's process).
	lock, err := vmDir.Lock()
	if err != nil {
		return err
	}
	acquired, err := lock.Trylock()
	if err != nil {
		return err
	}
	if !acquired {
		return weaveerrors.ErrVMAlreadyRunning("VM \"%s\" is already running!", c.Name)
	}

	// Now the VM state will return "running", so we can unlock.
	if err := storageLock.Unlock(); err != nil {
		return err
	}

	runCtx, cancelRun := context.WithCancel(context.Background())

	go c.driveVM(runCtx, localStorage, vmDir, vncImpl, vmConfig)

	// Serve disk-snapshot requests for this running VM (`weave snapshot create`,
	// the REST API): the run process owns the VZ handle, so it performs the
	// pause/clone/resume.
	go snapshot.Serve(runCtx, vmDir, liveSnapshotHandler{s: c, dir: vmDir})

	// Enable in-process snapshot revert (rebuild the VM and re-point the window
	// in place, no relaunch). Disabled for VNC runs, whose server is bound to the
	// original VM instance; those fall back to the relaunch path.
	c.inProcessRevertReady = !(c.VNC || c.VNCExperimental)
	ui.RevertFunc = c.TriggerRevert

	// "weave stop" support.
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT)
	go func() {
		<-sigint
		cancelRun()
	}()

	// "weave suspend" / UI window closing support.
	sigusr1 := make(chan os.Signal, 1)
	signal.Notify(sigusr1, syscall.SIGUSR1)
	go func() {
		for range sigusr1 {
			c.suspendVM(vmDir, cancelRun)
		}
	}()

	// Graceful shutdown support: for macOS guests this brings up a dialog
	// asking the user if they are sure they want to shut down.
	sigusr2 := make(chan os.Signal, 1)
	signal.Notify(sigusr2, syscall.SIGUSR2)
	go func() {
		for range sigusr2 {
			fmt.Println("Requesting guest OS to stop...")
			_ = c.vm.RequestStop()
		}
	}()

	ui.SetRunInfo(c.buildRunInfo())

	useVNCWithoutGraphics := (c.VNC || c.VNCExperimental) && !c.Graphics
	if c.NoGraphics || useVNCWithoutGraphics {
		// Enter the main event loop without bringing up any UI, waiting for
		// the VM to exit.
		ui.RunHeadless()
	} else {
		(&ui.Window{
			VM:                c.vm,
			CaptureSystemKeys: c.CaptureSystemKeys,
			Suspendable:       c.Suspendable,
			VMDir:             vmDir.BaseURL,
		}).Run()
	}

	return nil
}

// buildVMInstance resolves a fresh set of run-time devices (serial PTYs, NIC
// attachments, extra/USB storage, directory shares) and constructs a new VM. It
// is used both for the initial run and to rebuild the VM for an in-process
// snapshot revert — the attachments are single-use, so each instance gets its
// own freshly-resolved set.
func (c *Session) buildVMInstance(vmDir *layout.VMDirectory, vmConfig *vmconfig.VMConfig) (*weavevm.VM, error) {
	var serialPorts []idvirt.SerialPortConfigurationProvider
	if c.Serial {
		ttyFD := weavevm.CreatePTY()
		if ttyFD < 0 {
			return nil, weaveerrors.ErrVMConfigurationError("Failed to create PTY")
		}
		ttyRead := foundation.NewFileHandleWithFileDescriptorCloseOnDealloc(ttyFD, false)
		ttyWrite := foundation.NewFileHandleWithFileDescriptorCloseOnDealloc(ttyFD, false)
		serialPorts = append(serialPorts, createSerialPortConfiguration(ttyRead, ttyWrite))
	} else if c.SerialPath != "" {
		ttyRead := foundation.FileHandleForReadingAtPath(c.SerialPath)
		ttyWrite := foundation.FileHandleForWritingAtPath(c.SerialPath)
		if ttyRead == nil || ttyWrite == nil {
			return nil, weaveerrors.ErrVMConfigurationError("Failed to open PTY")
		}
		serialPorts = append(serialPorts, createSerialPortConfiguration(ttyRead, ttyWrite))
	}

	// Dedicated serial channel for the resident clipboard agent (in-session,
	// announces itself over this port). Added whenever the clipboard engine will
	// run, independent of the user's --serial console.
	if c.clipboardRun {
		agentPort, err := c.clipboardAgentSerialPort()
		if err != nil {
			return nil, err
		}
		serialPorts = append(serialPorts, agentPort)
	}

	// Parse root disk options.
	diskOptions := parseDiskOptions(c.RootDiskOpts)
	syncMode, err := parseDiskImageSynchronizationMode(diskOptions.syncModeRaw)
	if err != nil {
		return nil, err
	}
	var caching *idvirt.DiskImageCachingMode
	if cachingMode, ok, err := parseDiskImageCachingMode(diskOptions.cachingModeRaw); err != nil {
		return nil, err
	} else if ok {
		caching = &cachingMode
	}

	nics, err := c.resolveNICs(vmConfig)
	if err != nil {
		return nil, err
	}
	c.primaryBridged = primaryNICIsBridged(nics)

	// Softnet needs the SUID bit (or passwordless sudo) before the helper can
	// be spawned; prompt for it interactively when any resolved NIC is softnet.
	if topologyNeedsSoftnet(nics) && isInteractiveSession() {
		if err := weavenetwork.SoftnetConfigureSUIDBitIfNeeded(); err != nil {
			return nil, err
		}
	}

	additionalStorageDevices, err := c.additionalDiskAttachments()
	if err != nil {
		return nil, err
	}
	usbStorageDevices, err := c.usbMassStorageDevices()
	if err != nil {
		return nil, err
	}
	additionalStorageDevices = append(additionalStorageDevices, usbStorageDevices...)
	directorySharingDevices, err := c.directoryShares()
	if err != nil {
		return nil, err
	}
	rosettaShares, err := c.rosettaDirectoryShare()
	if err != nil {
		return nil, err
	}
	directorySharingDevices = append(directorySharingDevices, rosettaShares...)

	return weavevm.NewVMWithConfig(vmDir, vmConfig, weavevm.VMOptions{
		NICs:                     nics,
		AdditionalStorageDevices: additionalStorageDevices,
		DirectorySharingDevices:  directorySharingDevices,
		SerialPorts:              serialPorts,
		Suspendable:              c.Suspendable,
		Nested:                   c.Nested,
		NoAudio:                  c.NoAudio,
		// Clipboard is off entirely when the user passed --no-clipboard or the
		// resolved policy is disabled; otherwise the engine owns it and SPICE
		// stays off (vm.go gates on ClipboardPolicyEnabled).
		NoClipboard:            c.NoClipboard || !c.clipboardPolicy.Active(),
		ClipboardPolicyEnabled: c.clipboardRun,
		Sync:                   syncMode,
		Caching:                caching,
		NoTrackpad:             c.NoTrackpad,
		NoPointer:              c.NoPointer,
		NoKeyboard:             c.NoKeyboard,
		// Bind the VM to the main queue when a UI surface touches it on the main
		// thread — the headed VZVirtualMachineView, or the VNC server (set on the
		// main queue). A pure headless run (--no-graphics, no VNC) leaves it on a
		// dedicated serial queue, off the main thread.
		MainQueue: !c.NoGraphics || c.VNC || c.VNCExperimental,
	})
}

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

// suspendVM ports the SIGUSR1 handler: snapshot the VM and shut down.
// resolveClipboard computes the effective enterprise clipboard policy for this
// run (CLI flags > per-VM config > settings default > built-in default) and
// records whether the engine should run plus the guest's OS/arch for agent
// deployment.
//
// The weave-guestd engine is the single clipboard mechanism: it runs by default
// (the built-in policy is enabled + bidirectional) for every guest OS, and owns
// the clipboard so the SPICE agent path is not also wired (vm.go gates SPICE on
// ClipboardPolicyEnabled). --no-clipboard, or a resolved policy that is disabled
// (e.g. settings/per-VM with direction=disabled), turns the clipboard off
// entirely — neither the engine nor SPICE runs.
func (c *Session) resolveClipboard(vmConfig *vmconfig.VMConfig) {
	override := c.ClipboardOverride
	if c.Clipboard {
		enabled := true
		override.Enabled = &enabled
	}

	var settingsDefault *clipboardpolicy.Policy
	if settings, err := weaveconfig.LoadSettings(); err == nil {
		settingsDefault = settings.DefaultClipboardPolicy
	}
	perVM := vmConfig.ClipboardPolicy

	policy := clipboardpolicy.Resolve(settingsDefault, perVM, override)
	c.clipboardPolicy = policy
	c.clipboardRun = !c.NoClipboard && policy.Active()
	c.guestGOOS = string(vmConfig.OS)
	c.guestGOARCH = string(vmConfig.Arch)
}

// serveClipboardControl runs the host control socket that lets `weave clipboard
// set` push live policy overrides onto this VM's running engine. Each request
// layers its override onto the engine's current policy, applies it live, and —
// when persist is set — writes the resulting policy to the VM config in place
// (a rename would invalidate this process's fcntl run-lock on config.json).
func (c *Session) serveClipboardControl(ctx context.Context, vmDir *layout.VMDirectory, engine *clipboard.Engine) {
	handler := func(req clipboardctl.Request) (clipboardpolicy.Policy, error) {
		// An empty override is a pure query (weave clipboard get): return the
		// current policy without touching the engine.
		if req.Override.IsZero() {
			return engine.Policy(), nil
		}
		updated := req.Override.Apply(engine.Policy())
		engine.SetPolicy(updated)
		if req.Persist {
			vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
			if err != nil {
				return updated, err
			}
			vmConfig.ClipboardPolicy = &updated
			if err := vmConfig.SaveInPlace(vmDir.ConfigURL()); err != nil {
				return updated, err
			}
		}
		return updated, nil
	}
	if err := clipboardctl.Serve(ctx, vmDir.ClipboardControlSocketURL(), handler); err != nil && ctx.Err() == nil {
		logging.LogError("clipboard control socket: %v", err)
	}
}

// buildRunInfo collects the applied launch-time options (those not persisted in
// the VM config) for the UI's VM Info panel.
func (c *Session) buildRunInfo() ui.RunInfo {
	return ui.RunInfo{
		Network:     c.networkSummary(),
		Clipboard:   c.clipboardSummary(),
		Disks:       c.Disk,
		Dirs:        c.Dir,
		SharedDirs:  c.SharedDir,
		USBStorage:  c.USBStorage,
		Rosetta:     c.RosettaTag,
		Suspendable: c.Suspendable,
		VNC:         c.VNC || c.VNCExperimental,
		Nested:      c.Nested,
		NoAudio:     c.NoAudio,
		NoTrackpad:  c.NoTrackpad,
		NoKeyboard:  c.NoKeyboard,
		NoPointer:   c.NoPointer,
		CaptureKeys: c.CaptureSystemKeys,
	}
}

func (c *Session) networkSummary() string {
	switch {
	case c.NetHost:
		return "host"
	case len(c.NetBridged) > 0:
		return "bridged: " + strings.Join(c.NetBridged, ", ")
	case c.NetSoftnet:
		return "softnet"
	case len(c.NetDevice) > 0:
		return "device: " + strings.Join(c.NetDevice, ", ")
	case c.NetProfile != "":
		return "profile: " + c.NetProfile
	default:
		return "" // VM Info renders this as "nat (default)"
	}
}

func (c *Session) clipboardSummary() string {
	if !c.clipboardRun || !c.clipboardPolicy.Active() {
		return "disabled"
	}
	parts := []string{string(c.clipboardPolicy.Direction)}
	if c.clipboardPolicy.FileTransfer {
		parts = append(parts, "files")
	}
	if c.clipboardPolicy.AuditLog {
		parts = append(parts, "audit")
	}
	return strings.Join(parts, " · ")
}

func (c *Session) suspendVM(vmDir *layout.VMDirectory, cancelRun context.CancelFunc) {
	if !weaveplatform.MacOSAtLeast(14) {
		fmt.Println(
			weaveerrors.ErrSuspendFailed(
				"this functionality is only supported on macOS 14 (Sonoma) or newer",
			),
		)
		telemetry.OTelShared().Flush()
		os.Exit(1)
	}

	validateErr := c.vm.ValidateSaveRestoreSupport()
	if validateErr != nil {
		// The running configuration can't be saved — typically the VM was
		// started without --suspendable, so it still carries USB input/entropy
		// devices, or its guest has no save/restore-compatible device set. The
		// VM has not been paused yet, so report the failure and leave it running
		// instead of tearing it down.
		fmt.Println(weaveerrors.ErrSuspendFailed(validateErr.Error()))
		return
	}

	fmt.Println("pausing VM to take a snapshot...")
	if err := c.vm.SendErrorCompletion("pauseWithCompletionHandler:"); err != nil {
		fmt.Println(weaveerrors.ErrSuspendFailed(err.Error()))
		telemetry.OTelShared().Flush()
		os.Exit(1)
	}
	fmt.Println("creating a snapshot...")
	if err := c.vm.SaveMachineStateTo(vmDir.StateURL()); err != nil {
		fmt.Println(weaveerrors.ErrSuspendFailed(err.Error()))
		telemetry.OTelShared().Flush()
		os.Exit(1)
	}

	fmt.Println("snapshot created successfully! shutting down the VM...")
	cancelRun()
}

func createSerialPortConfiguration(
	ttyRead *foundation.FileHandle,
	ttyWrite *foundation.FileHandle,
) idvirt.SerialPortConfigurationProvider {
	attachment := idvirt.NewFileHandleSerialPortAttachmentWithFileHandleForReadingFileHandleForWriting(
		ttyRead,
		ttyWrite,
	)
	return idvirt.NewVirtioConsoleDeviceSerialPortConfiguration().WithAttachment(attachment)
}

// clipboardAgentSerialPort builds the dedicated virtio serial port the resident
// clipboard agent talks over, bridged to the host with two pipes (mirroring
// VirtualBuddy's Pipe()-backed VZFileHandleSerialPortAttachment). The host ends
// are stored on c and reused across VM rebuilds; fresh FileHandles wrap the same
// fds each build (an attachment consumes its handles).
//
// Per VZFileHandleSerialPortAttachment semantics — data written to
// fileHandleForReading goes to the guest, guest output appears on
// fileHandleForWriting — the VM reads the host→guest pipe and writes the
// guest→host pipe; the host writes the former and reads the latter.
func (c *Session) clipboardAgentSerialPort() (idvirt.SerialPortConfigurationProvider, error) {
	if c.clipSerialHostR == nil || c.clipSerialHostW == nil {
		var h2g, g2h [2]int // [read, write]
		if err := syscall.Pipe(h2g[:]); err != nil {
			return nil, weaveerrors.ErrVMConfigurationError("clipboard serial pipe: %v", err)
		}
		if err := syscall.Pipe(g2h[:]); err != nil {
			return nil, weaveerrors.ErrVMConfigurationError("clipboard serial pipe: %v", err)
		}
		// VM ends stay raw/blocking (handed to the framework as bare fds); host
		// ends become *os.File so Go's runtime poller drives their I/O.
		c.clipSerialVMReadFD = h2g[0]  // framework reads -> to guest
		c.clipSerialVMWriteFD = g2h[1] // framework writes guest output here
		c.clipSerialHostW = os.NewFile(uintptr(h2g[1]), "weave-clip-h2g-w")
		c.clipSerialHostR = os.NewFile(uintptr(g2h[0]), "weave-clip-g2h-r")
	}
	ttyRead := foundation.NewFileHandleWithFileDescriptorCloseOnDealloc(c.clipSerialVMReadFD, false)
	ttyWrite := foundation.NewFileHandleWithFileDescriptorCloseOnDealloc(c.clipSerialVMWriteFD, false)
	return createSerialPortConfiguration(ttyRead, ttyWrite), nil
}

func isInteractiveSession() bool {
	return terminal.TermIsTerminal()
}

// resolveNICs resolves the run's network topology into a concrete NIC list.
// Precedence: --net-profile, then --net-device, then the legacy --net-* flags,
// then the VM config's persisted NICs (a single NAT NIC for legacy configs).
func (c *Session) resolveNICs(vmConfig *vmconfig.VMConfig) ([]vmconfig.NICConfig, error) {
	switch {
	case c.NetProfile != "":
		return weavenetwork.ExpandProfile(c.NetProfile, weavenetwork.ProfileOptions{
			BridgedInterface: firstOrEmpty(c.NetBridged),
			SoftnetExpose:    c.NetSoftnetExpose,
		})

	case len(c.NetDevice) > 0:
		return weavenetwork.ParseNICDevices(c.NetDevice)

	case len(c.NetBridged) > 0:
		nics := make([]vmconfig.NICConfig, 0, len(c.NetBridged))
		for i, name := range c.NetBridged {
			if _, found := weavenetwork.FindBridgedInterface(name); !found {
				return nil, weaveerrors.ErrGeneric(
					"no bridge interfaces matched %q, available interfaces: %s",
					name,
					strings.Join(weavenetwork.BridgeInterfaces(), ", "),
				)
			}
			nics = append(nics, vmconfig.NICConfig{
				Mode:             vmconfig.NICModeBridged,
				IsPrimary:        i == 0,
				BridgedInterface: name,
			})
		}
		return nics, nil

	case c.NetSoftnet, c.NetHost:
		return []vmconfig.NICConfig{{
			Mode:            vmconfig.NICModeSoftnet,
			IsPrimary:       true,
			SoftnetHostMode: c.NetHost,
			SoftnetAllow:    c.NetSoftnetAllow,
			SoftnetBlock:    c.NetSoftnetBlock,
			SoftnetExpose:   c.NetSoftnetExpose,
		}}, nil

	default:
		// Use the already-loaded config rather than reopening config.json: in the
		// run process that file holds the fcntl PID lock, and reopening it would
		// drop the lock.
		return vmConfig.EnsureNICs(), nil
	}
}

// primaryNICIsBridged reports whether the primary NIC is bridged, so the VNC
// layer resolves the guest IP via ARP rather than DHCP.
func primaryNICIsBridged(nics []vmconfig.NICConfig) bool {
	for _, nic := range nics {
		if nic.IsPrimary {
			return nic.Mode == vmconfig.NICModeBridged
		}
	}
	return len(nics) > 0 && nics[0].Mode == vmconfig.NICModeBridged
}

// topologyNeedsSoftnet reports whether any NIC uses the softnet engine.
func topologyNeedsSoftnet(nics []vmconfig.NICConfig) bool {
	for _, nic := range nics {
		if nic.Mode == vmconfig.NICModeSoftnet {
			return true
		}
	}
	return false
}

// firstOrEmpty returns the first element of s, or "".
func firstOrEmpty(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

// additionalDiskAttachments ports Run.additionalDiskAttachments().
func (c *Session) additionalDiskAttachments() ([]idvirt.StorageDeviceConfigurationProvider, error) {
	var configurations []idvirt.StorageDeviceConfigurationProvider
	for _, disk := range c.Disk {
		configuration, err := craftAdditionalDisk(disk)
		if err != nil {
			return nil, err
		}
		configurations = append(configurations, configuration)
	}
	return configurations, nil
}

// usbMassStorageDevices ports lume's --usb-storage: each image is attached
// read-write as a USB mass storage device (macOS 13+).
func (c *Session) usbMassStorageDevices() ([]idvirt.StorageDeviceConfigurationProvider, error) {
	if len(c.USBStorage) == 0 {
		return nil, nil
	}
	if !weaveplatform.MacOSAtLeast(13) {
		return nil, weavevm.NewUnsupportedOSError("USB mass storage devices", "are")
	}

	var configurations []idvirt.StorageDeviceConfigurationProvider
	for _, imagePath := range c.USBStorage {
		attachment, err := idvirt.NewDiskImageStorageDeviceAttachmentWithURLReadOnlyError(
			objcutil.ExpandTilde(imagePath), false)
		if err != nil {
			return nil, err
		}
		configurations = append(
			configurations,
			idvirt.NewUSBMassStorageDeviceConfigurationWithAttachment(
				storageBase(attachment),
			),
		)
	}
	return configurations, nil
}

// diskOptions ports Run.swift's DiskOptions struct.
type diskOptions struct {
	readOnly              bool
	syncModeRaw           string
	cachingModeRaw        string
	foundAtLeastOneOption bool
}

func parseDiskOptions(parseFrom string) diskOptions {
	var options diskOptions

	for option := range strings.SplitSeq(parseFrom, ",") {
		switch {
		case option == "ro":
			options.readOnly = true
			options.foundAtLeastOneOption = true
		case strings.HasPrefix(option, "sync="):
			options.syncModeRaw = strings.TrimPrefix(option, "sync=")
			options.foundAtLeastOneOption = true
		case strings.HasPrefix(option, "caching="):
			options.cachingModeRaw = strings.TrimPrefix(option, "caching=")
			options.foundAtLeastOneOption = true
		}
	}

	return options
}

// craftAdditionalDisk ports Run.swift's AdditionalDisk: a disk image path,
// block device, remote VM name or NBD URL with optional :options suffix.
func craftAdditionalDisk(parseFrom string) (idvirt.StorageDeviceConfigurationProvider, error) {
	diskPath, options := parseAdditionalDiskOptions(parseFrom)

	syncMode, err := parseDiskSynchronizationMode(options.syncModeRaw)
	if err != nil {
		return nil, err
	}

	// Network Block Devices.
	if scheme, _, ok := strings.Cut(diskPath, "://"); ok &&
		(scheme == "nbd" || scheme == "nbds" || scheme == "nbd+unix" || scheme == "nbds+unix") {
		if !weaveplatform.MacOSAtLeast(14) {
			return nil, weavevm.NewUnsupportedOSError("attaching Network Block Devices", "are")
		}

		nbdAttachment, err := idvirt.NewNetworkBlockDeviceStorageDeviceAttachmentWithURLTimeoutForcedReadOnlySynchronizationModeError(
			diskPath,
			30,
			options.readOnly,
			syncMode,
		)
		if err != nil {
			return nil, err
		}
		return idvirt.NewVirtioBlockDeviceConfigurationWithAttachment(
			storageBase(nbdAttachment),
		), nil
	}

	// Expand the tilde (~) since at this point we're dealing with a local
	// path; doing it earlier corrupts remote URLs like nbd://.
	expandedDiskPath := objcutil.ExpandTilde(diskPath)

	// Block devices.
	if pathHasMode(expandedDiskPath, syscall.S_IFBLK) {
		if !weaveplatform.MacOSAtLeast(14) {
			return nil, weavevm.NewUnsupportedOSError("attaching block devices", "are")
		}

		openMode := os.O_RDWR
		if options.readOnly {
			openMode = os.O_RDONLY
		}
		fd, err := syscall.Open(expandedDiskPath, openMode, 0)
		if err != nil {
			switch {
			case errors.Is(err, syscall.EBUSY):
				return nil, weaveerrors.ErrFailedToOpenBlockDevice(
					expandedDiskPath,
					"already in use, try umounting it via \"diskutil unmountDisk\" (when the whole disk) or \"diskutil umount\" (when mounting a single partition)",
				)
			case errors.Is(err, syscall.EACCES):
				return nil, weaveerrors.ErrFailedToOpenBlockDevice(
					expandedDiskPath,
					fmt.Sprintf(
						"permission denied, consider changing the disk's owner using \"sudo chown $USER %s\" or run Weave as a superuser (see --disk help for more details on how to do that correctly)",
						expandedDiskPath,
					),
				)
			default:
				return nil, weaveerrors.ErrFailedToOpenBlockDevice(expandedDiskPath, err.Error())
			}
		}

		fileHandle := foundation.NewFileHandleWithFileDescriptorCloseOnDealloc(fd, true)
		blockAttachment, err := idvirt.NewDiskBlockDeviceStorageDeviceAttachmentWithFileHandleReadOnlySynchronizationModeError(
			fileHandle,
			options.readOnly,
			syncMode,
		)
		if err != nil {
			return nil, err
		}
		return idvirt.NewVirtioBlockDeviceConfigurationWithAttachment(
			storageBase(blockAttachment),
		), nil
	}

	// Support remote VM names in the --disk command-line argument.
	if remoteName, err := oci.NewRemoteName(diskPath); err == nil {
		ociStorage, err := vmstorage.NewVMStorageOCI()
		if err != nil {
			return nil, err
		}
		vmDir, err := ociStorage.Open(remoteName, time.Now())
		if err != nil {
			return nil, err
		}

		// VZDiskImageStorageDeviceAttachment does not support FileHandle,
		// so clone the disk into an intermediate tmp location.
		config, err := weaveconfig.NewConfig()
		if err != nil {
			return nil, err
		}
		clonedDiskPath := filepath.Join(config.WeaveTmpDir, "run-disk-"+fsutil.UUID())

		if err := fsutil.CopyItem(vmDir.DiskURL(), clonedDiskPath); err != nil {
			return nil, err
		}

		cloneLock, err := weavelock.NewFileLock(clonedDiskPath)
		if err != nil {
			return nil, err
		}
		if err := cloneLock.Lock(); err != nil {
			return nil, err
		}

		diskImageAttachment, err := idvirt.NewDiskImageStorageDeviceAttachmentWithURLReadOnlyError(
			clonedDiskPath, options.readOnly)
		if err != nil {
			return nil, err
		}
		return idvirt.NewVirtioBlockDeviceConfigurationWithAttachment(
			storageBase(diskImageAttachment),
		), nil
	}

	// Error out if the disk is locked by the host (e.g. it was mounted in
	// Finder), see cirruslabs/tart#323.
	if !options.readOnly {
		diskLock, err := weavelock.NewFileLock(expandedDiskPath)
		if err == nil {
			acquired, lockErr := diskLock.Trylock()
			if lockErr == nil && !acquired {
				_ = diskLock.Close()
				return nil, weaveerrors.ErrDiskAlreadyInUse(
					"disk %s seems to be already in use, unmount it first in Finder",
					expandedDiskPath,
				)
			}
			_ = diskLock.Close()
		}
	}

	cachingMode := idvirt.DiskImageCachingModeAutomatic
	if mode, ok, err := parseDiskImageCachingMode(options.cachingModeRaw); err != nil {
		return nil, err
	} else if ok {
		cachingMode = mode
	}
	imageSyncMode, err := parseDiskImageSynchronizationMode(options.syncModeRaw)
	if err != nil {
		return nil, err
	}

	diskImageAttachment, err := idvirt.NewDiskImageStorageDeviceAttachmentWithURLReadOnlyCachingModeSynchronizationModeError(
		expandedDiskPath,
		options.readOnly,
		cachingMode,
		imageSyncMode,
	)
	if err != nil {
		return nil, err
	}
	return idvirt.NewVirtioBlockDeviceConfigurationWithAttachment(
		storageBase(diskImageAttachment),
	), nil
}

// parseAdditionalDiskOptions ports AdditionalDisk.parseOptions(_:).
func parseAdditionalDiskOptions(parseFrom string) (string, diskOptions) {
	arguments := strings.Split(parseFrom, ":")

	options := parseDiskOptions(arguments[len(arguments)-1])
	if options.foundAtLeastOneOption {
		arguments = arguments[:len(arguments)-1]
	}

	return strings.Join(arguments, ":"), options
}

// directoryShare ports Run.swift's DirectoryShare struct.
type directoryShare struct {
	name     string
	path     string // local path or http(s) URL
	readOnly bool
	mountTag string
}

func parseDirectoryShare(parseFrom string) directoryShare {
	share := directoryShare{
		mountTag: idvirt.MacOSGuestAutomountTag(),
	}

	// Consume options.
	arguments := strings.Split(parseFrom, ":")
	found := false
	for option := range strings.SplitSeq(arguments[len(arguments)-1], ",") {
		switch {
		case option == "ro":
			share.readOnly = true
			found = true
		case strings.HasPrefix(option, "tag="):
			share.mountTag = strings.TrimPrefix(option, "tag=")
			found = true
		}
	}
	if found {
		arguments = arguments[:len(arguments)-1]
	}
	rest := strings.Join(arguments, ":")

	// Special case for URLs.
	if strings.HasPrefix(rest, "http:") || strings.HasPrefix(rest, "https:") {
		share.path = rest
		return share
	}

	if name, path, ok := strings.Cut(rest, ":"); ok {
		share.name = name
		share.path = objcutil.ExpandTilde(path)
	} else {
		share.path = objcutil.ExpandTilde(rest)
	}
	return share
}

// parseSharedDirectoryShare parses lume's --shared-dir syntax
// (path[:ro|rw], default read-write) into the same directoryShare struct as
// --dir. The macOS automount tag is always used.
func parseSharedDirectoryShare(parseFrom string) (directoryShare, error) {
	share := directoryShare{
		mountTag: idvirt.MacOSGuestAutomountTag(),
	}

	path := parseFrom
	if prefix, suffix, ok := strings.Cut(parseFrom, ":"); ok {
		switch suffix {
		case "ro":
			share.readOnly = true
		case "rw":
			share.readOnly = false
		default:
			return share, weaveerrors.ErrGeneric(
				"invalid --shared-dir format: expected <path>[:ro|rw], got %q",
				parseFrom,
			)
		}
		path = prefix
	}
	if path == "" {
		return share, weaveerrors.ErrGeneric(
			"invalid --shared-dir format: expected <path>[:ro|rw], got %q",
			parseFrom,
		)
	}
	share.path = objcutil.ExpandTilde(path)
	return share, nil
}

// directoryShares ports Run.directoryShares(), extended with lume's
// --shared-dir entries which funnel into the same sharing devices.
func (c *Session) directoryShares() ([]idvirt.DirectorySharingDeviceConfigurationProvider, error) {
	if len(c.Dir) == 0 && len(c.SharedDir) == 0 {
		return nil, nil
	}

	if !weaveplatform.MacOSAtLeast(13) {
		return nil, weavevm.NewUnsupportedOSError("directory sharing", "is")
	}

	allShares := make([]directoryShare, 0, len(c.Dir)+len(c.SharedDir))
	for _, rawDir := range c.Dir {
		allShares = append(allShares, parseDirectoryShare(rawDir))
	}
	for _, rawDir := range c.SharedDir {
		share, err := parseSharedDirectoryShare(rawDir)
		if err != nil {
			return nil, err
		}
		allShares = append(allShares, share)
	}

	sharesByTag := map[string][]directoryShare{}
	var tagOrder []string
	for _, share := range allShares {
		if _, ok := sharesByTag[share.mountTag]; !ok {
			tagOrder = append(tagOrder, share.mountTag)
		}
		sharesByTag[share.mountTag] = append(sharesByTag[share.mountTag], share)
	}

	var devices []idvirt.DirectorySharingDeviceConfigurationProvider
	for _, mountTag := range tagOrder {
		shares := sharesByTag[mountTag]

		sharingDevice := idvirt.NewVirtioFileSystemDeviceConfigurationWithTag(mountTag)

		allNamedShares := true
		for _, share := range shares {
			if share.name == "" {
				allNamedShares = false
			}
		}

		if len(shares) == 1 && shares[0].name == "" {
			sharedDirectory, err := shares[0].createConfiguration()
			if err != nil {
				return nil, err
			}
			sharingDevice.WithShare(
				idvirt.NewSingleDirectoryShareWithDirectory(sharedDirectory),
			)
		} else if !allNamedShares {
			return nil, weaveerrors.ErrGeneric(
				"invalid --dir syntax: for multiple directory shares each one of them should be named",
			)
		} else {
			directories := foundation.NewMutableDictionary()
			for _, share := range shares {
				sharedDirectory, err := share.createConfiguration()
				if err != nil {
					return nil, err
				}
				directories.SetString(share.name, sharedDirectory)
			}
			sharingDevice.WithShare(idvirt.NewMultipleDirectoryShareWithDirectories(directories))
		}

		devices = append(devices, sharingDevice)
	}

	return devices, nil
}

// createConfiguration ports DirectoryShare.createConfiguration(): local
// paths are shared directly; remote archives are downloaded (with an
// on-disk cache, mirroring Swift's URLCache usage) and unpacked into a
// temporary directory with tar.
func (s directoryShare) createConfiguration() (*idvirt.SharedDirectory, error) {
	if !strings.HasPrefix(s.path, "http:") && !strings.HasPrefix(s.path, "https:") {
		return idvirt.NewSharedDirectoryWithURLReadOnly(s.path, s.readOnly), nil
	}

	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}

	// Cache the downloaded archive by URL digest.
	cachePath := filepath.Join(config.WeaveCacheDir,
		"dir-archive-"+strings.TrimPrefix(oci.DigestHash([]byte(s.path)), "sha256:")+".tgz")

	if _, err := os.Stat(cachePath); err != nil {
		fmt.Printf("Downloading %s...\n", s.path)
		chunks, response, err := fetcher.FetcherFetch(context.Background(),
			fetcher.FetchRequest{URL: s.path}, true)
		if err != nil {
			return nil, err
		}

		// Known-size transfer: disk-space guard up front, then percentage
		// progress (the spinner is only for indeterminate waits — see
		// terminal/spinner.go).
		var progress *logging.DownloadProgress
		if expectedLength := response.ContentLength; expectedLength > 0 {
			if err := vmstorage.EnsureDiskSpace(uint64(expectedLength), nil); err != nil {
				return nil, err
			}
			progress = logging.NewDownloadProgress(expectedLength)
			logging.NewProgressObserver(progress).Log(logging.DefaultLogger())
		}

		archive, err := os.Create(cachePath)
		if err != nil {
			return nil, err
		}
		empty := true
		for chunk := range chunks {
			if chunk.Err != nil {
				archive.Close()
				_ = os.Remove(cachePath)
				return nil, chunk.Err
			}
			if len(chunk.Data) > 0 {
				empty = false
			}
			if _, err := archive.Write(chunk.Data); err != nil {
				archive.Close()
				_ = os.Remove(cachePath)
				return nil, err
			}
			if progress != nil {
				progress.Add(int64(len(chunk.Data)))
			}
		}
		if err := archive.Close(); err != nil {
			return nil, err
		}
		if empty {
			_ = os.Remove(cachePath)
			return nil, weaveerrors.ErrGeneric("Remote archive is empty!")
		}
		fmt.Println("Cached for future invocations!")
	} else {
		fmt.Printf("Using cached archive for %s...\n", s.path)
	}

	temporaryPath := filepath.Join(config.WeaveTmpDir, fsutil.UUID()+".volume")
	if err := os.MkdirAll(temporaryPath, 0o755); err != nil {
		return nil, err
	}
	tmpLock, err := weavelock.NewFileLock(temporaryPath)
	if err != nil {
		return nil, err
	}
	if err := tmpLock.Lock(); err != nil {
		return nil, err
	}

	if _, err := exec.LookPath("tar"); err != nil {
		return nil, weaveerrors.ErrGeneric("tar not found in PATH")
	}

	cmd := exec.Command("tar", "-xzf", cachePath)
	cmd.Dir = temporaryPath
	if err := cmd.Run(); err != nil {
		return nil, weaveerrors.ErrGeneric("Unarchiving failed!")
	}

	fmt.Println("Unarchived into a temporary directory!")

	return idvirt.NewSharedDirectoryWithURLReadOnly(temporaryPath, s.readOnly), nil
}

// rosettaDirectoryShare ports Run.rosettaDirectoryShare().
func (c *Session) rosettaDirectoryShare() ([]idvirt.DirectorySharingDeviceConfigurationProvider, error) {
	if c.RosettaTag == "" {
		return nil, nil
	}
	if runtime.GOARCH != "arm64" {
		// There is no Rosetta on Intel.
		return nil, nil
	}
	if !weaveplatform.MacOSAtLeast(13) {
		return nil, weavevm.NewUnsupportedOSError("Rosetta directory share", "is")
	}

	switch idvirt.Availability() {
	case idvirt.LinuxRosettaAvailabilityNotInstalled:
		return nil, &weavevm.UnsupportedOSError{
			What:     "Rosetta directory share",
			Plural:   "is",
			Requires: "that have Rosetta installed",
		}
	case idvirt.LinuxRosettaAvailabilityNotSupported:
		return nil, &weavevm.UnsupportedOSError{
			What:     "Rosetta directory share",
			Plural:   "is",
			Requires: "running Apple silicon",
		}
	}

	if err := idvirt.ValidateTag(c.RosettaTag); err != nil {
		return nil, err
	}
	device := idvirt.NewVirtioFileSystemDeviceConfigurationWithTag(c.RosettaTag).
		WithShare(idvirt.NewLinuxRosettaDirectoryShare())

	return []idvirt.DirectorySharingDeviceConfigurationProvider{device}, nil
}

// pathHasMode ports Run.swift's pathHasMode(_:mode:).
func pathHasMode(path string, mode uint16) bool {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false
	}
	return st.Mode&syscall.S_IFMT == mode
}

// storageBase narrows a concrete disk/NBD/block storage attachment to the base
// VZStorageDeviceAttachment type the block/USB device constructors expect.
func storageBase(a obj.Object) *idvirt.StorageDeviceAttachment {
	base, _ := obj.As(a, "VZStorageDeviceAttachment", idvirt.StorageDeviceAttachmentFromID)
	return base
}
