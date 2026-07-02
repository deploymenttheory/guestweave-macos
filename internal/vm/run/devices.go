// Device resolution: turning the CLI's disk/share/NIC/USB/rosetta specs and
// VZ enum flag values into freshly-resolved virtual devices for each VM
// build (attachments are single-use).
//go:build darwin

package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/fsutil"
	weavelock "github.com/deploymenttheory/guestweave/internal/lock"
	weavenetwork "github.com/deploymenttheory/guestweave/internal/network"
	"github.com/deploymenttheory/guestweave/internal/objcutil"
	"github.com/deploymenttheory/guestweave/internal/oci"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
	weavevm "github.com/deploymenttheory/guestweave/internal/vm"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
	vmstorage "github.com/deploymenttheory/guestweave/internal/vm/storage"
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
		// Known-size transfer: disk-space guard up front, then percentage
		// progress (the spinner is only for indeterminate waits — see
		// terminal/spinner.go).
		_, written, err := vmstorage.FetchToFile(context.Background(), s.path,
			func(string) string { return cachePath }, vmstorage.FetchToFileOptions{})
		if err != nil {
			return nil, err
		}
		if written == 0 {
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
