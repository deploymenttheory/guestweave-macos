// Port of tart's VM.swift: wraps VZVirtualMachine, crafts its configuration,
// drives start/stop/install, and receives delegate callbacks.
//
// Swift-to-Go mechanics:
//   - VZVirtualMachineDelegate is implemented by registering an ObjC class
//     at runtime through purego (vmDelegateClass).
//   - Generated *CompletionHandler bindings panic under purego, so every
//     async call builds its block manually (see fetcher.go for the pattern).
//   - VZVirtualMachine is queue-confined: all of its access goes through vm.do,
//     which runs on the VM's dispatch queue (the main queue when headed, a
//     dedicated serial queue otherwise).
//go:build darwin

package vm

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/deploymenttheory/guestweave/internal/telemetry"
	vmstorage "github.com/deploymenttheory/guestweave/internal/vm/storage"

	"github.com/deploymenttheory/guestweave/internal/controlsocket"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"

	"github.com/deploymenttheory/guestweave/internal/ci"
	"github.com/deploymenttheory/guestweave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/fetcher"
	"github.com/deploymenttheory/guestweave/internal/fsutil"
	"github.com/deploymenttheory/guestweave/internal/ipsw"
	"github.com/deploymenttheory/guestweave/internal/logging"
	weavenetwork "github.com/deploymenttheory/guestweave/internal/network"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
	"github.com/deploymenttheory/guestweave/internal/prune"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
	idfoundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	idiomatic "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	mainthread "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/tools/grandcentraldispatch/mainthread"
	serialqueue "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/tools/grandcentraldispatch/serialqueue"

	ebiobjc "github.com/ebitengine/purego/objc"
)

// Error types ported from VM.swift.

type unsupportedRestoreImageError struct{}

func (unsupportedRestoreImageError) Error() string { return "unsupported restore image" }

// UnsupportedOSError ports VM.swift's UnsupportedOSError.
type UnsupportedOSError struct {
	What     string
	Plural   string
	Requires string
}

func NewUnsupportedOSError(what string, plural string) *UnsupportedOSError {
	return &UnsupportedOSError{What: what, Plural: plural, Requires: "running macOS 13.0 (Ventura) or newer"}
}

func (e *UnsupportedOSError) Error() string {
	return fmt.Sprintf("error: %s %s only supported on hosts %s", e.What, e.Plural, e.Requires)
}

type unsupportedArchitectureError struct{}

func (unsupportedArchitectureError) Error() string { return "unsupported architecture" }

// VMOptions carries VM.init's defaulted parameters. Zero values match the
// Swift defaults (audio and clipboard are inverted to keep that true).
type VMOptions struct {
	// NICs optionally overrides the VM config's persisted network topology for
	// this run (nil uses the config's NICs, synthesised from the legacy MAC
	// when absent). The primary NIC's MAC is bound from the VM config; missing
	// MACs on secondary NICs are derived deterministically.
	NICs                     []vmconfig.NICConfig
	AdditionalStorageDevices []idiomatic.StorageDeviceConfigurationProvider
	DirectorySharingDevices  []idiomatic.DirectorySharingDeviceConfigurationProvider
	SerialPorts              []idiomatic.SerialPortConfigurationProvider
	Suspendable              bool
	Nested                   bool
	NoAudio                  bool
	NoClipboard              bool
	// ClipboardPolicyEnabled means the host-side enterprise clipboard engine is
	// authoritative; the SPICE agent clipboard is disabled so policy controls
	// (direction, formats, files, bandwidth) are not bypassed by the OS path.
	ClipboardPolicyEnabled bool
	Sync                   idiomatic.DiskImageSynchronizationMode
	Caching                *idiomatic.DiskImageCachingMode
	NoTrackpad             bool
	NoPointer              bool
	NoKeyboard             bool

	// MainQueue binds the VZVirtualMachine to the process main queue instead of a
	// dedicated serial queue. Required for a headed run, where the VM is shown in
	// a VZVirtualMachineView (an @MainActor NSView) that must share the main
	// queue. Headless runs and `create` leave it false so VM work runs off the
	// main thread on its own serial queue.
	MainQueue bool
}

func (o *VMOptions) normalize() {
	if o.Sync == 0 {
		o.Sync = idiomatic.DiskImageSynchronizationModeFull
	}
}

// resolveTopology builds the network topology for a run: it uses the override
// NICs when present, otherwise the config's NICs (synthesised from the legacy
// MAC when absent), then binds the primary NIC's MAC to the config's address so
// guest IP resolution and MAC-conflict detection stay correct.
func resolveTopology(vmConfig *vmconfig.VMConfig, options VMOptions) (*weavenetwork.Topology, error) {
	nics := options.NICs
	if nics == nil {
		nics = vmConfig.EnsureNICs()
	} else {
		nics = append([]vmconfig.NICConfig(nil), nics...)
	}

	primaryMAC := vmConfig.MACAddress.String()
	boundPrimary := false
	for i := range nics {
		if nics[i].IsPrimary && !boundPrimary {
			nics[i].MACAddress = primaryMAC
			boundPrimary = true
		}
	}
	if !boundPrimary && len(nics) > 0 {
		nics[0].IsPrimary = true
		nics[0].MACAddress = primaryMAC
	}

	return weavenetwork.BuildTopology(nics)
}

// VM ports tart's VM class.
type VM struct {
	// VirtualMachine is Virtualization.Framework's virtual machine.
	VirtualMachine *idiomatic.VirtualMachine

	// Configuration is the machine's VZVirtualMachineConfiguration.
	Configuration *idiomatic.VirtualMachineConfiguration

	// sema communicates with the VZVirtualMachineDelegate.
	sema *weavenetwork.AsyncSemaphore

	Name   string
	Config *vmconfig.VMConfig

	network    *weavenetwork.Topology
	delegateID purego.ID

	// exec runs a closure on the queue the VZVirtualMachine is confined to — the
	// main queue (headed) or vq (headless). All VZVirtualMachine calls go through
	// vm.do so they stay on that one queue. vq is nil in the main-queue case.
	exec func(func())
	vq   *serialqueue.Queue
}

// do runs fn on the VM's dispatch queue and blocks until it returns.
// VZVirtualMachine is queue-confined (it must be used on the queue it was created
// on), so every VZVirtualMachine call must go through here rather than running on
// an arbitrary goroutine.
func (vm *VM) do(fn func()) { vm.exec(fn) }

var _ controlsocket.VirtioSocketConnector = (*VM)(nil)

// vmDelegateRegistry maps delegate instances to their VM.
var vmDelegateRegistry sync.Map // purego.ID → *VM

// vmDelegateClass registers the ObjC class implementing
// VZVirtualMachineDelegate, signalling the VM's semaphore like VM.swift.
var vmDelegateClass = sync.OnceValue(func() purego.Class {
	lookup := func(self purego.ID) *VM {
		if vm, ok := vmDelegateRegistry.Load(self); ok {
			return vm.(*VM)
		}
		return nil
	}

	class, err := purego.RegisterClass("OrinVMDelegate", purego.GetClass("NSObject"),
		[]*purego.Protocol{purego.GetProtocol("VZVirtualMachineDelegate")},
		nil,
		[]purego.MethodDef{
			{
				Cmd: purego.RegisterName("guestDidStop:"),
				Fn: func(self purego.ID, _ purego.SEL, _ purego.ID) {
					fmt.Println("guest has stopped the virtual machine")
					if vm := lookup(self); vm != nil {
						vm.sema.Signal()
					}
				},
			},
			{
				Cmd: purego.RegisterName("virtualMachine:didStopWithError:"),
				Fn: func(self purego.ID, _ purego.SEL, _ purego.ID, errID purego.ID) {
					fmt.Printf("guest has stopped the virtual machine due to error: %v\n", purego.NSErrorToError(errID))
					if vm := lookup(self); vm != nil {
						vm.sema.Signal()
					}
				},
			},
			{
				Cmd: purego.RegisterName("virtualMachine:networkDevice:attachmentWasDisconnectedWithError:"),
				Fn: func(self purego.ID, _ purego.SEL, _ purego.ID, deviceID purego.ID, errID purego.ID) {
					fmt.Printf("virtual machine's network attachment %v has been disconnected with error: %v\n",
						deviceID, purego.NSErrorToError(errID))
					if vm := lookup(self); vm != nil {
						vm.sema.Signal()
					}
				},
			},
		})
	if err != nil {
		panic(fmt.Sprintf("failed to register OrinVMDelegate: %v", err))
	}
	return class
})

// newVM ports VM.init(vmDir:…) for an existing VM directory.
func newVM(vmDir *layout.VMDirectory, options VMOptions) (*VM, error) {
	config, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return nil, err
	}
	return NewVMWithConfig(vmDir, config, options)
}

// NewVMWithConfig builds a VM from an already-loaded config, avoiding a re-read
// of config.json. The run process holds an fcntl PID lock on config.json, and
// reopening that file from the same process drops the lock (POSIX: closing any
// descriptor to the file releases the process's locks on it). The in-process
// snapshot-revert rebuild therefore passes the config it already has rather than
// going through NewVM.
func NewVMWithConfig(vmDir *layout.VMDirectory, config *vmconfig.VMConfig, options VMOptions) (*VM, error) {
	options.normalize()

	if config.Arch != weaveplatform.CurrentArchitecture() {
		return nil, unsupportedArchitectureError{}
	}

	topology, err := resolveTopology(config, options)
	if err != nil {
		return nil, err
	}

	configuration, err := craftConfiguration(vmDir.DiskURL(), vmDir.NvramURL(), config, options, topology)
	if err != nil {
		return nil, err
	}

	vm := &VM{
		Configuration: configuration,
		sema:          weavenetwork.NewAsyncSemaphore(),
		Name:          vmDir.Name(),
		Config:        config,
		network:       topology,
	}
	vm.attachVirtualMachine(options.MainQueue)

	return vm, nil
}

// attachVirtualMachine creates the VZVirtualMachine on its dispatch queue and
// installs the delegate (Swift: VZVirtualMachine(configuration:queue:) +
// delegate). When mainQueue is set the VM is bound to the main queue (headed
// runs, where the VZVirtualMachineView shares it); otherwise it gets a dedicated
// serial queue so its work runs off the main thread. The delegate's callbacks
// then fire on whichever queue the VM owns, and just signal vm.sema.
func (vm *VM) attachVirtualMachine(mainQueue bool) {
	if mainQueue {
		vm.exec = mainthread.Do
	} else {
		vm.vq = serialqueue.New("com.deploymenttheory.guestweave.vm")
		vm.exec = vm.vq.Do
	}

	vm.do(func() {
		if vm.vq != nil {
			queue := obj.Wrap(ebiobjc.ID(vm.vq.Handle()))
			vm.VirtualMachine = idiomatic.NewVirtualMachineWithConfigurationQueue(vm.Configuration, queue)
		} else {
			vm.VirtualMachine = idiomatic.NewVirtualMachineWithConfiguration(vm.Configuration)
		}

		delegateID := purego.ID(vmDelegateClass()).Send(purego.RegisterName("new"))
		vmDelegateRegistry.Store(delegateID, vm)
		vm.delegateID = delegateID
		obj.ID(vm.VirtualMachine).Send(purego.RegisterName("setDelegate:"), delegateID)
	})
}

// retrieveIPSW ports VM.retrieveIPSW(remoteURL:): returns a cached *.ipsw
// location, downloading and caching it when missing.
func retrieveIPSW(ctx context.Context, remoteURLString string) (string, error) {
	cache, err := ipsw.NewIPSWCache()
	if err != nil {
		return "", err
	}

	// Check if we already have this IPSW in cache.
	_, headResponse, err := fetcher.FetcherFetch(ctx, fetcher.FetchRequest{URL: remoteURLString, Method: "HEAD"}, false)
	if err != nil {
		return "", err
	}

	if hash := headResponse.Header.Get("x-amz-meta-digest-sha256"); hash != "" {
		ipswLocation := cache.LocationFor("sha256:" + hash + ".ipsw")

		if fsutil.Exists(ipswLocation) {
			logging.DefaultLogger().AppendNewLine("Using cached *.ipsw file...")
			if err := prune.UpdateAccessDate(ipswLocation, time.Now()); err != nil {
				return "", err
			}
			return ipswLocation, nil
		}
	}

	// Download the IPSW.
	logging.DefaultLogger().AppendNewLine(fmt.Sprintf("Fetching %s...", filepath.Base(remoteURLString)))

	finalLocation, _, err := vmstorage.FetchToFile(ctx, remoteURLString,
		func(digest string) string { return cache.LocationFor(digest + ".ipsw") },
		vmstorage.FetchToFileOptions{AlwaysProgress: true})
	return finalLocation, err
}

// InFinalState ports VM.inFinalState.
func (vm *VM) InFinalState() bool {
	state := vm.machineState()
	return state == idiomatic.VirtualMachineStateStopped ||
		state == idiomatic.VirtualMachineStatePaused ||
		state == idiomatic.VirtualMachineStateError
}

func (vm *VM) machineState() idiomatic.VirtualMachineState {
	var state idiomatic.VirtualMachineState
	vm.do(func() {
		state = vm.VirtualMachine.State()
	})
	return state
}

// NewVMInstallingFromIPSW ports the arm64-only VM.init(vmDir:ipswURL:…):
// creates NVRAM, disk and config from a restore image, then runs the
// automated macOS installation.
func NewVMInstallingFromIPSW(ctx context.Context, vmDir *layout.VMDirectory, ipswLocation string,
	diskSizeGB uint16, diskFormat diskimage.DiskImageFormat, options VMOptions) (*VM, error) {
	ctx, span := otel.Tracer("weave").Start(ctx, "vm.create_from_ipsw",
		trace.WithAttributes(attribute.String("vm.name", vmDir.Name())))
	defer span.End()
	telemetry.OTelShared().Instruments.VMOperations.Add(ctx, 1,
		metric.WithAttributes(attribute.String("operation", "create"), attribute.String("vm.name", vmDir.Name())))

	options.normalize()

	ipswPath := ipswLocation
	if isRemoteIPSW(ipswLocation) {
		downloaded, err := retrieveIPSW(ctx, ipswLocation)
		if err != nil {
			return nil, err
		}
		ipswPath = downloaded
	}

	// The Virtualization.Framework cannot deal with paths that contain
	// symlinks, so expand them first.
	if resolved, err := filepath.EvalSymlinks(ipswPath); err == nil {
		ipswPath = resolved
	}

	// Load the restore image and get the requirements that match both the
	// image and our platform.
	image, err := loadMacOSRestoreImage(ctx, ipswPath)
	if err != nil {
		return nil, err
	}

	requirements := image.MostFeaturefulSupportedConfiguration()
	if requirements == nil {
		return nil, unsupportedRestoreImageError{}
	}

	// Create NVRAM.
	if _, err := idiomatic.NewMACAuxiliaryStorageCreatingStorageAtURLHardwareModelOptionsError(
		vmDir.NvramURL(), requirements.HardwareModel(), 0); err != nil {
		return nil, err
	}

	// Create disk.
	if err := vmDir.ResizeDisk(diskSizeGB, diskFormat); err != nil {
		return nil, err
	}

	// Create config.
	ecid := idiomatic.NewMacMachineIdentifier()
	config := vmconfig.NewVMConfig(
		vmconfig.NewDarwinPlatform(ecid, requirements.HardwareModel()),
		int(requirements.MinimumSupportedCPUCount()),
		requirements.MinimumSupportedMemorySize(),
		nil,
		diskFormat,
	)
	// Allocate at least 4 CPUs because otherwise VMs are frequently freezing.
	if err := config.SetCPU(max(4, int(requirements.MinimumSupportedCPUCount()))); err != nil {
		return nil, err
	}
	if err := config.Save(vmDir.ConfigURL()); err != nil {
		return nil, err
	}

	topology, err := resolveTopology(config, options)
	if err != nil {
		return nil, err
	}

	configuration, err := craftConfiguration(vmDir.DiskURL(), vmDir.NvramURL(), config, options, topology)
	if err != nil {
		return nil, err
	}

	vm := &VM{
		Configuration: configuration,
		sema:          weavenetwork.NewAsyncSemaphore(),
		Name:          vmDir.Name(),
		Config:        config,
		network:       topology,
	}
	vm.attachVirtualMachine(options.MainQueue)

	// Run automated installation.
	if err := vm.install(ctx, ipswPath); err != nil {
		return nil, err
	}

	return vm, nil
}

// isRemoteIPSW reports whether the IPSW location is an http(s) URL (as opposed
// to a local filesystem path).
func isRemoteIPSW(location string) bool {
	return strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://")
}

// loadMacOSRestoreImage loads a restore image from a local *.ipsw path.
func loadMacOSRestoreImage(ctx context.Context, ipswPath string) (*idiomatic.MacOSRestoreImage, error) {
	return idiomatic.LoadFileURL(ctx, ipswPath)
}

// install ports VM.install(_:): runs VZMacOSInstaller with progress logging.
func (vm *VM) install(ctx context.Context, ipswPath string) error {
	var installer *idiomatic.MacOSInstaller
	vm.do(func() {
		installer = idiomatic.NewMACOSInstallerWithVirtualMachineRestoreImageURL(
			vm.VirtualMachine, ipswPath)
	})

	logging.DefaultLogger().AppendNewLine("Installing OS...")
	observer := logging.NewProgressObserver(&nsProgressWrapper{inner: idfoundation.ProgressFromID(obj.ID(installer.Progress()))})
	observer.Log(logging.DefaultLogger())

	errCh := make(chan error, 1)
	block := purego.NewBlock(func(_ purego.Block, errID purego.ID) {
		if errID != 0 {
			errCh <- purego.NSErrorToError(errID)
		} else {
			errCh <- nil
		}
	})
	vm.do(func() {
		obj.ID(installer).Send(purego.RegisterName("installWithCompletionHandler:"), block)
	})

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		if p, ok := obj.As(installer.Progress(), "NSProgress", idfoundation.ProgressFromID); ok {
			p.Cancel()
		}
		return <-errCh
	}
}

// VMLinux ports VM.linux(vmDir:diskSizeGB:diskFormat:).
func VMLinux(vmDir *layout.VMDirectory, diskSizeGB uint16, diskFormat diskimage.DiskImageFormat) (*VM, error) {
	// Create NVRAM.
	if _, err := idiomatic.NewEFIVariableStoreCreatingVariableStoreAtURLOptionsError(vmDir.NvramURL(), 0); err != nil {
		return nil, err
	}

	// Create disk.
	if err := vmDir.ResizeDisk(diskSizeGB, diskFormat); err != nil {
		return nil, err
	}

	// Create config.
	config := vmconfig.NewVMConfig(&vmconfig.LinuxPlatform{}, 4, 4096*1024*1024, nil, diskFormat)
	if err := config.Save(vmDir.ConfigURL()); err != nil {
		return nil, err
	}

	return newVM(vmDir, VMOptions{})
}

// Start ports VM.start(recovery:resume:).
func (vm *VM) Start(recovery bool, shouldResume bool) error {
	ctx, span := otel.Tracer("weave").Start(context.Background(), "vm.start",
		trace.WithAttributes(
			attribute.String("vm.name", vm.Name),
			attribute.Bool("vm.recovery", recovery),
		))
	defer span.End()
	telemetry.OTelShared().Instruments.VMOperations.Add(ctx, 1,
		metric.WithAttributes(attribute.String("operation", "start"), attribute.String("vm.name", vm.Name)))

	if err := vm.network.Run(vm.sema); err != nil {
		span.RecordError(err)
		return err
	}

	if shouldResume {
		return vm.resumeMachine()
	}
	return vm.startMachine(recovery)
}

// Connect ports VM.connect(toPort:); it satisfies the controlsocket.VirtioSocketConnector
// interface used by ControlSocket.
func (vm *VM) Connect(ctx context.Context, toPort uint32) (*idiomatic.VirtioSocketConnection, error) {
	var socketDeviceID purego.ID
	vm.do(func() {
		devices := vm.VirtualMachine.SocketDevices()
		if len(devices) > 0 {
			socketDeviceID = obj.ID(devices[0])
		}
	})

	if socketDeviceID == 0 {
		return nil, weaveerrors.ErrVMSocketFailed(toPort, ", VM has no socket devices configured")
	}

	isVirtio := purego.Send[bool](socketDeviceID, purego.RegisterName("isKindOfClass:"),
		purego.GetClass("VZVirtioSocketDevice"))
	if !isVirtio {
		return nil, weaveerrors.ErrVMSocketFailed(toPort, ", expected VM's first socket device to have a type of VZVirtioSocketDevice")
	}

	type result struct {
		connection *idiomatic.VirtioSocketConnection
		err        error
	}
	resultCh := make(chan result, 1)
	block := purego.NewBlock(func(_ purego.Block, connectionID purego.ID, errID purego.ID) {
		if errID != 0 {
			resultCh <- result{err: purego.NSErrorToError(errID)}
			return
		}
		resultCh <- result{connection: idiomatic.VirtioSocketConnectionFromID(purego.Retain(connectionID))}
	})
	vm.do(func() {
		socketDeviceID.Send(purego.RegisterName("connectToPort:completionHandler:"), toPort, block)
	})

	select {
	case r := <-resultCh:
		return r.connection, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Run ports VM.run(): waits for the delegate (or cancellation), stops the VM
// gracefully when cancelled, then stops the network.
func (vm *VM) Run(ctx context.Context) error {
	// A cancellation here is triggered by "weave stop", Ctrl+C, or closing
	// the VM window, so shut down the VM gracefully below.
	_ = vm.sema.WaitUnlessCancelled(ctx)

	if ctx.Err() != nil {
		if vm.machineState() == idiomatic.VirtualMachineStateRunning {
			fmt.Println("Stopping VM...")
			if err := vm.stopMachine(); err != nil {
				return err
			}
		}
	}

	return vm.network.Stop()
}

// StartMachine starts the underlying virtual machine without re-running network
// setup, backing the Control → Start menu item (ports MainApp's Start button,
// Run.swift:849, which calls vm.virtualMachine.start()).
func (vm *VM) StartMachine(recovery bool) error {
	return vm.startMachine(recovery)
}

// startMachine ports the @MainActor VM.start(_ recovery:).
func (vm *VM) startMachine(recovery bool) error {
	errCh := make(chan error, 1)
	block := purego.NewBlock(func(_ purego.Block, errID purego.ID) {
		if errID != 0 {
			errCh <- purego.NSErrorToError(errID)
		} else {
			errCh <- nil
		}
	})

	vm.do(func() {
		startOptions := idiomatic.NewMacOSVirtualMachineStartOptions()
		startOptions.WithStartUpFromMACOSRecovery(recovery)
		obj.ID(vm.VirtualMachine).Send(
			purego.RegisterName("startWithOptions:completionHandler:"), obj.ID(startOptions), block)
	})

	return <-errCh
}

// resumeMachine ports the @MainActor VM.resume().
func (vm *VM) resumeMachine() error {
	return vm.SendErrorCompletion("resumeWithCompletionHandler:")
}

// stopMachine ports the @MainActor VM.stop().
func (vm *VM) stopMachine() error {
	return vm.SendErrorCompletion("stopWithCompletionHandler:")
}

// RequestStop asks the guest OS to shut down cleanly
// (VZVirtualMachine.requestStop), dispatched onto the VM's queue.
func (vm *VM) RequestStop() error {
	var err error
	vm.do(func() { err = vm.VirtualMachine.RequestStop() })
	return err
}

// ValidateSaveRestoreSupport reports whether the running configuration can be
// saved and restored, dispatched onto the VM's queue.
func (vm *VM) ValidateSaveRestoreSupport() error {
	var err error
	vm.do(func() { err = vm.Configuration.ValidateSaveRestoreSupport() })
	return err
}

func (vm *VM) SendErrorCompletion(selector string) error {
	errCh := make(chan error, 1)
	block := purego.NewBlock(func(_ purego.Block, errID purego.ID) {
		if errID != 0 {
			errCh <- purego.NSErrorToError(errID)
		} else {
			errCh <- nil
		}
	})
	vm.do(func() {
		obj.ID(vm.VirtualMachine).Send(purego.RegisterName(selector), block)
	})
	return <-errCh
}

// craftConfiguration ports VM.craftConfiguration(…). It assembles the machine
// configuration through the idiomatic fluent builders; path arguments are plain
// filesystem paths (the idiomatic constructors take string URLs).
func craftConfiguration(diskPath string, nvramPath string,
	vmConfig *vmconfig.VMConfig, options VMOptions, topology *weavenetwork.Topology) (*idiomatic.VirtualMachineConfiguration, error) {
	configuration := idiomatic.NewVirtualMachineConfiguration()

	// Boot loader.
	bootLoader, err := vmConfig.Platform.BootLoader(nvramPath)
	if err != nil {
		return nil, err
	}

	// vmconfig.Platform.
	platform, err := vmConfig.Platform.Platform(nvramPath, options.Nested)
	if err != nil {
		return nil, err
	}

	configuration.
		WithBootLoader(bootLoader).
		WithCPUCount(int(vmConfig.CPUCount)).
		WithMemorySize(vmConfig.MemorySize).
		WithPlatform(platform).
		WithGraphicsDevices(vmConfig.Platform.GraphicsDevice(vmConfig))

	// On macOS 14 the framework's save/restore (VZ suspend) accepted only a
	// limited device set — no host audio, no entropy device, and the Mac
	// keyboard/trackpad in place of USB input. As of macOS 15 save/restore
	// supports the full device set, so "suspendable" no longer drops anything
	// there. This also matters for correctness: a state saved with the full set
	// fails to restore against the limited set (VZErrorDomain:12 invalid
	// argument), so the save-time and restore-time configs must agree.
	limitForSuspend := options.Suspendable && !weaveplatform.MacOSAtLeast(15)

	// Audio.
	soundDeviceConfiguration := idiomatic.NewVirtioSoundDeviceConfiguration()
	if !options.NoAudio && !limitForSuspend {
		inputStream := idiomatic.NewVirtioSoundDeviceInputStreamConfiguration().
			WithSource(idiomatic.NewHostAudioInputStreamSource())
		outputStream := idiomatic.NewVirtioSoundDeviceOutputStreamConfiguration().
			WithSink(idiomatic.NewHostAudioOutputStreamSink())
		soundDeviceConfiguration.WithStreams(inputStream, outputStream)
	} else {
		// Just a null speaker.
		soundDeviceConfiguration.WithStreams(idiomatic.NewVirtioSoundDeviceOutputStreamConfiguration())
	}
	configuration.WithAudioDevices(soundDeviceConfiguration)

	// Keyboard and mouse.
	suspendablePlatform, isSuspendable := vmConfig.Platform.(vmconfig.PlatformSuspendable)
	if limitForSuspend && isSuspendable {
		// Some guests (Linux) have no save/restore-compatible input devices, so
		// their suspendable device sets are empty. Skip the setters in that case:
		// the idiomatic With* collection setters dereference a nil array for the
		// empty slice (SIGSEGV), and a fresh configuration already defaults these
		// device lists to empty.
		if keyboards := suspendablePlatform.KeyboardsSuspendable(); len(keyboards) > 0 {
			configuration.WithKeyboards(keyboards...)
		}
		if pointingDevices := suspendablePlatform.PointingDevicesSuspendable(); len(pointingDevices) > 0 {
			configuration.WithPointingDevices(pointingDevices...)
		}
	} else {
		if options.NoKeyboard {
			configuration.WithKeyboards()
		} else {
			configuration.WithKeyboards(vmConfig.Platform.Keyboards()...)
		}

		switch {
		case options.NoPointer:
			configuration.WithPointingDevices()
		case options.NoTrackpad:
			configuration.WithPointingDevices(vmConfig.Platform.PointingDevicesSimplified()...)
		default:
			configuration.WithPointingDevices(vmConfig.Platform.PointingDevices()...)
		}
	}

	// Networking: one VZVirtioNetworkDeviceConfiguration per NIC, each with its
	// own attachment and MAC address (multi-NIC with per-NIC properties).
	networkDevices := make([]idiomatic.NetworkDeviceConfigurationProvider, 0, len(topology.NICs()))
	for _, nic := range topology.NICs() {
		networkDevices = append(networkDevices, idiomatic.NewVirtioNetworkDeviceConfiguration().
			WithAttachment(nic.Attachment).
			WithMACAddress(nic.MAC))
	}
	configuration.WithNetworkDevices(networkDevices...)

	consoleDevices := make([]idiomatic.ConsoleDeviceConfigurationProvider, 0, 2)

	// Clipboard sharing via Spice agent. Skipped when the enterprise clipboard
	// engine owns the clipboard, so its policy is the single source of truth.
	if !options.NoClipboard && !options.ClipboardPolicyEnabled {
		spiceAgentPortAttachment := idiomatic.NewSpiceAgentPortAttachment()
		spiceAgentPortAttachment.WithSharesClipboard(true)
		spiceAgentPort := idiomatic.NewVirtioConsolePortConfiguration().
			WithName(idiomatic.SpiceAgentPortName()).
			WithAttachment(spiceAgentPortAttachment)
		spiceAgentConsoleDevice := idiomatic.NewVirtioConsoleDeviceConfiguration()
		setConsolePort(spiceAgentConsoleDevice, 0, spiceAgentPort)
		consoleDevices = append(consoleDevices, spiceAgentConsoleDevice)
	}

	// Storage.
	cachingMode := idiomatic.DiskImageCachingModeAutomatic
	if vmConfig.OS == weaveplatform.OSLinux {
		// When not specified, use "cached" caching mode for Linux VMs to
		// prevent file-system corruption (cirruslabs/tart#675).
		cachingMode = idiomatic.DiskImageCachingModeCached
	}
	if options.Caching != nil {
		cachingMode = *options.Caching
	}
	attachment, err := idiomatic.NewDiskImageStorageDeviceAttachmentWithURLReadOnlyCachingModeSynchronizationModeError(
		diskPath, false, cachingMode, options.Sync)
	if err != nil {
		return nil, err
	}

	storageDevices := make([]idiomatic.StorageDeviceConfigurationProvider, 0, 1+len(options.AdditionalStorageDevices))
	// The block-device constructor takes the base storage-attachment type; the
	// disk-image attachment is one, so narrow it to the base.
	blockAttachment, _ := obj.As(attachment, "VZStorageDeviceAttachment", idiomatic.StorageDeviceAttachmentFromID)
	storageDevices = append(storageDevices,
		idiomatic.NewVirtioBlockDeviceConfigurationWithAttachment(blockAttachment))
	storageDevices = append(storageDevices, options.AdditionalStorageDevices...)
	configuration.WithStorageDevices(storageDevices...)

	// Entropy.
	if !limitForSuspend {
		configuration.WithEntropyDevices(idiomatic.NewVirtioEntropyDeviceConfiguration())
	}

	// Directory sharing devices.
	configuration.WithDirectorySharingDevices(options.DirectorySharingDevices...)

	// Serial ports.
	configuration.WithSerialPorts(options.SerialPorts...)

	// Version console device: a dummy console device useful for implementing
	// host feature checks in the guest agent software. The "tart-version-"
	// port name is a wire contract — the Tart Guest Agent running inside
	// guest images discovers the host by this exact prefix, so it must not
	// be renamed to weave.
	consolePort := idiomatic.NewVirtioConsolePortConfiguration()
	consolePort.WithName("tart-version-" + ci.CIVersion())
	consoleDevice := idiomatic.NewVirtioConsoleDeviceConfiguration()
	setConsolePort(consoleDevice, 0, consolePort)
	consoleDevices = append(consoleDevices, consoleDevice)

	configuration.WithConsoleDevices(consoleDevices...)

	// Socket device.
	configuration.WithSocketDevices(idiomatic.NewVirtioSocketDeviceConfiguration())

	if err := configuration.Validate(); err != nil {
		return nil, err
	}

	return configuration, nil
}

// setConsolePort mirrors Swift's consoleDevice.ports[0] = port subscript.
func setConsolePort(device *idiomatic.VirtioConsoleDeviceConfiguration, index uint, port *idiomatic.VirtioConsolePortConfiguration) {
	device.Ports().SetObjectAtIndexedSubscript(port, int(index))
}
