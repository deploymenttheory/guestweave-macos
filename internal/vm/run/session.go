// Package run owns the run workflow (a port of tart's Commands/Run.swift):
// it boots a VM, optionally with a UI window, VNC, additional disks,
// directory shares and custom networking, and serves the sockets other weave
// commands use to talk to the running VM. The CLI layer parses flags into an
// Options and hands the process's main thread to Run.
//
// The run session: Options (the CLI-resolved run parameters), the Session
// runtime state, flag/environment validation, and the main-thread workflow
// entry (runMainThread ends in the AppKit run loop).
//go:build darwin

package run

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	"github.com/deploymenttheory/guestweave/internal/clipboard"
	"github.com/deploymenttheory/guestweave/internal/clipboardpolicy"
	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	"github.com/deploymenttheory/guestweave/internal/controlsocket"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	weavelock "github.com/deploymenttheory/guestweave/internal/lock"
	weavenetwork "github.com/deploymenttheory/guestweave/internal/network"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
	"github.com/deploymenttheory/guestweave/internal/terminal"
	"github.com/deploymenttheory/guestweave/internal/ui"
	weavevm "github.com/deploymenttheory/guestweave/internal/vm"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
	"github.com/deploymenttheory/guestweave/internal/vm/snapshot"
	vmstorage "github.com/deploymenttheory/guestweave/internal/vm/storage"
	weavevnc "github.com/deploymenttheory/guestweave/internal/vnc"
	"github.com/deploymenttheory/guestweave/internal/winimage"
)

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

	// Reporter receives the workflow's observable events (status lines, VNC
	// URL, clipboard health, UI hooks). The CLI supplies the default
	// stdout/ui implementation; nil falls back to a no-op.
	Reporter Reporter
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
	if opts.Reporter == nil {
		opts.Reporter = nopReporter{}
	}
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
			c.Reporter.Linef("There is already a running VM with the same MAC address!")
			c.Reporter.Linef("Resetting VM to assign a new MAC address...")
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
	c.Reporter.BindRevertHandler(c.TriggerRevert)

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
			c.Reporter.Linef("Requesting guest OS to stop...")
			_ = c.vm.RequestStop()
		}
	}()

	c.Reporter.RunInfoResolved(c.buildRunInfo())

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

func isInteractiveSession() bool {
	return terminal.TermIsTerminal()
}
