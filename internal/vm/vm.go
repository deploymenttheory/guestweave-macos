// Port of tart's VM.swift: wraps VZVirtualMachine, crafts its configuration,
// drives start/stop/install, and receives delegate callbacks.
//
// Swift-to-Go mechanics:
//   - VZVirtualMachineDelegate is implemented by registering an ObjC class
//     at runtime through purego (vmDelegateClass).
//   - Generated *CompletionHandler bindings panic under purego, so every
//     async call builds its block manually (see fetcher.go for the pattern).
//   - All VZVirtualMachine access is dispatched to the main queue via
//     mainthread.Do (opinionated/custom/mainthread), matching the @MainActor annotations.
//go:build darwin

package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/deploymenttheory/weave/internal/telemetry"
	"github.com/deploymenttheory/weave/internal/vmstorage"

	"github.com/deploymenttheory/weave/internal/controlsocket"
	"github.com/deploymenttheory/weave/internal/vmconfig"

	"github.com/deploymenttheory/weave/internal/ci"
	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/fetcher"
	"github.com/deploymenttheory/weave/internal/fsutil"
	"github.com/deploymenttheory/weave/internal/ipsw"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	"github.com/deploymenttheory/weave/internal/logging"
	weavenetwork "github.com/deploymenttheory/weave/internal/network"
	"github.com/deploymenttheory/weave/internal/oci"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/prune"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
	mainthread "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/custom/mainthread"
	idfoundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	idiomatic "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
)

// Error types ported from VM.swift.

type UnsupportedRestoreImageError struct{}

func (UnsupportedRestoreImageError) Error() string { return "unsupported restore image" }

type NoMainScreenFoundError struct{}

func (NoMainScreenFoundError) Error() string { return "no main screen found" }

type DownloadFailedError struct{}

func (DownloadFailedError) Error() string { return "download failed" }

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

type UnsupportedArchitectureError struct{}

func (UnsupportedArchitectureError) Error() string { return "unsupported architecture" }

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
}

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

// NewVM ports VM.init(vmDir:…) for an existing VM directory.
func NewVM(vmDir *vmdirectory.VMDirectory, options VMOptions) (*VM, error) {
	options.normalize()

	config, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return nil, err
	}

	if config.Arch != weaveplatform.CurrentArchitecture() {
		return nil, UnsupportedArchitectureError{}
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
	vm.attachVirtualMachine()

	return vm, nil
}

// attachVirtualMachine creates the VZVirtualMachine on the main queue and
// installs the delegate (Swift: VZVirtualMachine(configuration:) + delegate).
func (vm *VM) attachVirtualMachine() {
	mainthread.Do(func() {
		vm.VirtualMachine = idiomatic.NewVirtualMachineWithConfiguration(vm.Configuration)

		delegateID := purego.ID(vmDelegateClass()).Send(purego.RegisterName("new"))
		vmDelegateRegistry.Store(delegateID, vm)
		vm.delegateID = delegateID
		obj.ID(vm.VirtualMachine).Send(purego.RegisterName("setDelegate:"), delegateID)
	})
}

// VMRetrieveIPSW ports VM.retrieveIPSW(remoteURL:): returns a cached *.ipsw
// location, downloading and caching it when missing.
func VMRetrieveIPSW(ctx context.Context, remoteURLString string) (string, error) {
	// Check if we already have this IPSW in cache.
	_, headResponse, err := fetcher.FetcherFetch(ctx, fetcher.FetchRequest{URL: remoteURLString, Method: "HEAD"}, false)
	if err != nil {
		return "", err
	}

	if hash := headResponse.Header.Get("x-amz-meta-digest-sha256"); hash != "" {
		cache, err := ipsw.NewIPSWCache()
		if err != nil {
			return "", err
		}
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

	chunks, response, err := fetcher.FetcherFetch(ctx, fetcher.FetchRequest{URL: remoteURLString}, true)
	if err != nil {
		return "", err
	}

	config, err := weaveconfig.NewConfig()
	if err != nil {
		return "", err
	}
	temporaryLocation := filepath.Join(config.WeaveTmpDir, fsutil.UUID()+".ipsw")

	// Refuse the download up front if the host volume cannot hold it
	// (prunable cache entries reclaimed first).
	if expectedLength := response.ContentLength; expectedLength > 0 {
		if err := vmstorage.EnsureDiskSpace(uint64(expectedLength), nil); err != nil {
			return "", err
		}
	}

	progress := logging.NewDownloadProgress(response.ContentLength)
	logging.NewProgressObserver(progress).Log(logging.DefaultLogger())

	temporaryPath := temporaryLocation
	temporaryFile, err := os.Create(temporaryPath)
	if err != nil {
		return "", err
	}
	defer temporaryFile.Close()

	lock, err := weavelock.NewFileLock(temporaryLocation)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := lock.Lock(); err != nil {
		return "", err
	}

	digest := oci.NewDigest()
	for chunk := range chunks {
		if chunk.Err != nil {
			return "", chunk.Err
		}
		if _, err := temporaryFile.Write(chunk.Data); err != nil {
			return "", err
		}
		digest.Update(chunk.Data)
		progress.Add(int64(len(chunk.Data)))
	}
	if err := temporaryFile.Close(); err != nil {
		return "", err
	}

	cache, err := ipsw.NewIPSWCache()
	if err != nil {
		return "", err
	}
	finalLocation := cache.LocationFor(digest.Finalize() + ".ipsw")

	// Swift uses FileManager.replaceItemAt; an atomic rename is equivalent.
	if err := os.Rename(temporaryPath, finalLocation); err != nil {
		return "", err
	}
	return finalLocation, nil
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
	mainthread.Do(func() {
		state = vm.VirtualMachine.State()
	})
	return state
}

// NewVMInstallingFromIPSW ports the arm64-only VM.init(vmDir:ipswURL:…):
// creates NVRAM, disk and config from a restore image, then runs the
// automated macOS installation.
func NewVMInstallingFromIPSW(ctx context.Context, vmDir *vmdirectory.VMDirectory, ipswLocation string,
	diskSizeGB uint16, diskFormat diskimage.DiskImageFormat, options VMOptions) (*VM, error) {
	ctx, span := otel.Tracer("weave").Start(ctx, "vm.create_from_ipsw",
		trace.WithAttributes(attribute.String("vm.name", vmDir.Name())))
	defer span.End()
	telemetry.OTelShared().Instruments.VMOperations.Add(ctx, 1,
		metric.WithAttributes(attribute.String("operation", "create"), attribute.String("vm.name", vmDir.Name())))

	options.normalize()

	ipswPath := ipswLocation
	if isRemoteIPSW(ipswLocation) {
		downloaded, err := VMRetrieveIPSW(ctx, ipswLocation)
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
		return nil, UnsupportedRestoreImageError{}
	}

	// Create NVRAM.
	if _, err := idiomatic.NewMacAuxiliaryStorageCreatingStorageAtURLHardwareModelOptionsError(
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
	vm.attachVirtualMachine()

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
	mainthread.Do(func() {
		installer = idiomatic.NewMacOSInstallerWithVirtualMachineRestoreImageURL(
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
	mainthread.Do(func() {
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
func VMLinux(vmDir *vmdirectory.VMDirectory, diskSizeGB uint16, diskFormat diskimage.DiskImageFormat) (*VM, error) {
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

	return NewVM(vmDir, VMOptions{})
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
	mainthread.Do(func() {
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
	mainthread.Do(func() {
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

	mainthread.Do(func() {
		startOptions := idiomatic.NewMacOSVirtualMachineStartOptions()
		startOptions.SetStartUpFromMacOSRecovery(recovery)
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

func (vm *VM) SendErrorCompletion(selector string) error {
	errCh := make(chan error, 1)
	block := purego.NewBlock(func(_ purego.Block, errID purego.ID) {
		if errID != 0 {
			errCh <- purego.NSErrorToError(errID)
		} else {
			errCh <- nil
		}
	})
	mainthread.Do(func() {
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

	// Audio.
	soundDeviceConfiguration := idiomatic.NewVirtioSoundDeviceConfiguration()
	if !options.NoAudio && !options.Suspendable {
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
	if options.Suspendable && isSuspendable {
		configuration.WithKeyboards(suspendablePlatform.KeyboardsSuspendable()...)
		configuration.WithPointingDevices(suspendablePlatform.PointingDevicesSuspendable()...)
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
		spiceAgentPortAttachment.SetSharesClipboard(true)
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
	if !options.Suspendable {
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
	consolePort.SetName("tart-version-" + ci.CIVersion())
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
