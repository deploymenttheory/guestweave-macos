// Port of tart's VMDirectory.swift: the on-disk layout of a single VM
// (config.json, disk.img, nvram.bin, state.vzvmsave, control.sock). Paths are
// plain strings managed with os/path/filepath and fsutil.
//go:build darwin

package vmdirectory

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/fsutil"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/prune"
	"github.com/deploymenttheory/weave/internal/vmconfig"

	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
)

// VMDirectoryState mirrors VMDirectory.State.
type VMDirectoryState string

const (
	VMDirectoryStateRunning   VMDirectoryState = "running"
	VMDirectoryStateSuspended VMDirectoryState = "suspended"
	VMDirectoryStateStopped   VMDirectoryState = "stopped"
)

// VMDirectory mirrors tart's VMDirectory struct.
type VMDirectory struct {
	BaseURL string
}

var _ prune.Prunable = (*VMDirectory)(nil)

func NewVMDirectory(baseURL string) *VMDirectory {
	return &VMDirectory{BaseURL: baseURL}
}

func (d *VMDirectory) ConfigURL() string { return filepath.Join(d.BaseURL, "config.json") }

func (d *VMDirectory) DiskURL() string { return filepath.Join(d.BaseURL, "disk.img") }

func (d *VMDirectory) NvramURL() string { return filepath.Join(d.BaseURL, "nvram.bin") }

func (d *VMDirectory) StateURL() string { return filepath.Join(d.BaseURL, "state.vzvmsave") }

func (d *VMDirectory) ManifestURL() string { return filepath.Join(d.BaseURL, "manifest.json") }

// ControlSocketURL is the VM's control socket path; ControlSocket.Run chdirs to
// its directory and binds the short relative name (104-byte sun_path limit).
func (d *VMDirectory) ControlSocketURL() string { return filepath.Join(d.BaseURL, "control.sock") }

func (d *VMDirectory) ExplicitlyPulledMark() string {
	return filepath.Join(d.BaseURL, ".explicitly-pulled")
}

// VNCEndpointPath is where a running VM with an experimental VNC server records
// its vnc:// URL, so other processes can connect by name. Removed when the VM stops.
func (d *VMDirectory) VNCEndpointPath() string {
	return filepath.Join(d.BaseURL, ".vnc-endpoint")
}

func (d *VMDirectory) Name() string { return filepath.Base(d.BaseURL) }

func (d *VMDirectory) Path() string { return d.BaseURL }

// Lock ports VMDirectory.lock().
func (d *VMDirectory) Lock() (*weavelock.PIDLock, error) {
	return weavelock.NewPIDLock(d.ConfigURL())
}

// Running ports VMDirectory.running(). A failure to instantiate the PIDLock is
// reported as "not running": the common reason is a race with delete (ENOENT),
// and a false positive is cheaper than crashing "list" on a busy machine.
func (d *VMDirectory) Running() (bool, error) {
	lock, err := d.Lock()
	if err != nil {
		return false, nil
	}
	defer lock.Close()

	pid, err := lock.PID()
	if err != nil {
		return false, err
	}
	return pid != 0, nil
}

// State ports VMDirectory.state().
func (d *VMDirectory) State() (VMDirectoryState, error) {
	running, err := d.Running()
	if err != nil {
		return "", err
	}
	if running {
		return VMDirectoryStateRunning, nil
	}
	if fsutil.Exists(d.StateURL()) {
		return VMDirectoryStateSuspended, nil
	}
	return VMDirectoryStateStopped, nil
}

// VMDirectoryTemporary ports VMDirectory.temporary().
func VMDirectoryTemporary() (*VMDirectory, error) {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}
	tmpDir := filepath.Join(config.WeaveTmpDir, fsutil.UUID())
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	return NewVMDirectory(tmpDir), nil
}

// VMDirectoryTemporaryDeterministic ports VMDirectory.temporaryDeterministic
// (key:): a tmp directory whose name is the MD5 hash of key.
func VMDirectoryTemporaryDeterministic(key string) (*VMDirectory, error) {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}
	hash := md5.Sum([]byte(key))
	tmpDir := filepath.Join(config.WeaveTmpDir, hex.EncodeToString(hash[:]))
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	return NewVMDirectory(tmpDir), nil
}

// Initialized ports VMDirectory.initialized.
func (d *VMDirectory) Initialized() bool {
	return fsutil.Exists(d.ConfigURL()) &&
		fsutil.Exists(d.DiskURL()) &&
		fsutil.Exists(d.NvramURL())
}

// Initialize ports VMDirectory.initialize(overwrite:).
func (d *VMDirectory) Initialize(overwrite bool) error {
	if !overwrite && d.Initialized() {
		return weaveerrors.ErrVMDirectoryAlreadyInitialized("VM directory is already initialized, preventing overwrite")
	}

	if err := os.MkdirAll(d.BaseURL, 0o755); err != nil {
		return err
	}

	_ = os.RemoveAll(d.ConfigURL())
	_ = os.RemoveAll(d.DiskURL())
	_ = os.RemoveAll(d.NvramURL())

	return nil
}

// Validate ports VMDirectory.validate(userFriendlyName:).
func (d *VMDirectory) Validate(userFriendlyName string) error {
	if !fsutil.Exists(d.BaseURL) {
		return weaveerrors.ErrVMDoesNotExist(userFriendlyName)
	}

	if !d.Initialized() {
		return weaveerrors.ErrVMMissingFiles("VM is missing some of its files (%s, %s or %s)",
			filepath.Base(d.ConfigURL()),
			filepath.Base(d.DiskURL()),
			filepath.Base(d.NvramURL()))
	}

	return nil
}

// Clone ports VMDirectory.clone(to:generateMAC:).
func (d *VMDirectory) Clone(to *VMDirectory, generateMAC bool) error {
	if err := fsutil.CopyItem(d.ConfigURL(), to.ConfigURL()); err != nil {
		return err
	}
	if err := fsutil.CopyItem(d.NvramURL(), to.NvramURL()); err != nil {
		return err
	}
	if err := fsutil.CopyItem(d.DiskURL(), to.DiskURL()); err != nil {
		return err
	}
	_ = fsutil.CopyItem(d.StateURL(), to.StateURL())

	// Re-generate MAC address.
	if generateMAC {
		return to.RegenerateMACAddress()
	}
	return nil
}

// MACAddress ports VMDirectory.macAddress().
func (d *VMDirectory) MACAddress() (string, error) {
	config, err := vmconfig.NewVMConfigFromURL(d.ConfigURL())
	if err != nil {
		return "", err
	}
	return objcutil.GoStr(config.MACAddress.String()), nil
}

// RegenerateMACAddress ports VMDirectory.regenerateMACAddress().
func (d *VMDirectory) RegenerateMACAddress() error {
	config, err := vmconfig.NewVMConfigFromURL(d.ConfigURL())
	if err != nil {
		return err
	}

	config.MACAddress = idvirt.RandomLocallyAdministeredAddress().Unwrap()
	// Cleanup state if any.
	_ = os.RemoveAll(d.StateURL())

	return config.Save(d.ConfigURL())
}

// ResizeDisk ports VMDirectory.resizeDisk(_:format:).
func (d *VMDirectory) ResizeDisk(sizeGB uint16, format diskimage.DiskImageFormat) error {
	if fsutil.Exists(d.DiskURL()) {
		return d.resizeExistingDisk(sizeGB)
	}
	return d.createDisk(sizeGB, format)
}

func (d *VMDirectory) resizeExistingDisk(sizeGB uint16) error {
	config, err := vmconfig.NewVMConfigFromURL(d.ConfigURL())
	if err != nil {
		return err
	}

	if config.DiskFormat == diskimage.DiskImageFormatASIF {
		return d.resizeASIFDisk(sizeGB)
	}
	return d.resizeRawDisk(sizeGB)
}

func (d *VMDirectory) resizeRawDisk(sizeGB uint16) error {
	f, err := os.OpenFile(d.DiskURL(), os.O_RDWR, 0)
	if err != nil {
		return err
	}

	currentDiskFileLength, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		f.Close()
		return err
	}
	desiredDiskFileLength := int64(uint64(sizeGB) * 1000 * 1000 * 1000)

	if desiredDiskFileLength < currentDiskFileLength {
		f.Close()
		return weaveerrors.ErrInvalidDiskSize("new disk size of %s should be larger than the current disk size of %s",
			ByteCountString(desiredDiskFileLength), ByteCountString(currentDiskFileLength))
	} else if desiredDiskFileLength > currentDiskFileLength {
		if err := f.Truncate(desiredDiskFileLength); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

func (d *VMDirectory) resizeASIFDisk(sizeGB uint16) error {
	diskImageInfo, err := diskimage.DiskutilImageInfo(d.DiskURL())
	if err != nil {
		return weaveerrors.ErrFailedToResizeDisk("%v", err)
	}

	currentSizeBytes, err := diskImageInfo.TotalBytes()
	if err != nil {
		return weaveerrors.ErrFailedToResizeDisk("%v", err)
	}
	desiredSizeBytes := uint64(sizeGB) * 1000 * 1000 * 1000

	if desiredSizeBytes < uint64(currentSizeBytes) {
		return weaveerrors.ErrInvalidDiskSize("New disk size of %s should be larger than the current disk size of %s",
			ByteCountString(int64(desiredSizeBytes)), ByteCountString(int64(currentSizeBytes)))
	} else if desiredSizeBytes > uint64(currentSizeBytes) {
		return d.performASIFResize(sizeGB)
	}
	return nil
}

func (d *VMDirectory) performASIFResize(sizeGB uint16) error {
	if _, err := exec.LookPath("diskutil"); err != nil {
		return weaveerrors.ErrFailedToResizeDisk("diskutil not found in PATH")
	}

	stdout, stderr, err := diskimage.DiskutilRun([]string{
		"image", "resize",
		"--size", fmt.Sprintf("%dG", sizeGB),
		d.DiskURL(),
	})
	if err != nil {
		return weaveerrors.ErrFailedToResizeDisk("Failed to resize ASIF disk image: %v", err)
	}
	_ = stdout
	_ = stderr
	return nil
}

func (d *VMDirectory) createDisk(sizeGB uint16, format diskimage.DiskImageFormat) error {
	if format == diskimage.DiskImageFormatASIF {
		return diskimage.DiskutilImageCreate(d.DiskURL(), sizeGB)
	}
	return d.createRawDisk(sizeGB)
}

func (d *VMDirectory) createRawDisk(sizeGB uint16) error {
	f, err := os.OpenFile(d.DiskURL(), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	desiredDiskFileLength := int64(uint64(sizeGB) * 1000 * 1000 * 1000)
	if err := f.Truncate(desiredDiskFileLength); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Delete ports VMDirectory.delete().
func (d *VMDirectory) Delete() error {
	lock, err := d.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()

	acquired, err := lock.Trylock()
	if err != nil {
		return err
	}
	if !acquired {
		return weaveerrors.ErrVMIsRunning(d.Name())
	}

	if err := os.RemoveAll(d.BaseURL); err != nil {
		return err
	}

	return lock.Unlock()
}

func (d *VMDirectory) AccessDate() (time.Time, error) {
	return prune.AccessDate(d.BaseURL)
}

func (d *VMDirectory) AllocatedSizeBytes() (int, error) {
	return d.sumComponents(func(p *prune.PrunableURL) (int, error) { return p.AllocatedSizeBytes() })
}

func (d *VMDirectory) AllocatedSizeGB() (int, error) {
	bytes, err := d.AllocatedSizeBytes()
	return bytes / 1000 / 1000 / 1000, err
}

func (d *VMDirectory) DeduplicatedSizeBytes() (int, error) {
	return d.sumComponents(func(p *prune.PrunableURL) (int, error) { return p.DeduplicatedSizeBytes() })
}

func (d *VMDirectory) DeduplicatedSizeGB() (int, error) {
	bytes, err := d.DeduplicatedSizeBytes()
	return bytes / 1000 / 1000 / 1000, err
}

func (d *VMDirectory) SizeBytes() (int, error) {
	return d.sumComponents(func(p *prune.PrunableURL) (int, error) { return p.SizeBytes() })
}

func (d *VMDirectory) SizeGB() (int, error) {
	bytes, err := d.SizeBytes()
	return bytes / 1000 / 1000 / 1000, err
}

// DiskSizeBytes ports VMDirectory.diskSizeBytes().
func (d *VMDirectory) DiskSizeBytes() (int, error) {
	config, err := vmconfig.NewVMConfigFromURL(d.ConfigURL())
	if err != nil {
		return 0, err
	}

	if config.DiskFormat == diskimage.DiskImageFormatASIF {
		info, err := diskimage.DiskutilImageInfo(d.DiskURL())
		if err != nil {
			return 0, err
		}
		return info.TotalBytes()
	}
	return d.SizeBytes()
}

func (d *VMDirectory) DiskSizeGB() (int, error) {
	bytes, err := d.DiskSizeBytes()
	return bytes / 1000 / 1000 / 1000, err
}

// MarkExplicitlyPulled ports VMDirectory.markExplicitlyPulled().
func (d *VMDirectory) MarkExplicitlyPulled() {
	_ = os.WriteFile(d.ExplicitlyPulledMark(), nil, 0o644)
}

// IsExplicitlyPulled ports VMDirectory.isExplicitlyPulled().
func (d *VMDirectory) IsExplicitlyPulled() bool {
	return fsutil.Exists(d.ExplicitlyPulledMark())
}

func (d *VMDirectory) sumComponents(size func(*prune.PrunableURL) (int, error)) (int, error) {
	total := 0
	for _, path := range []string{d.ConfigURL(), d.DiskURL(), d.NvramURL()} {
		n, err := size(prune.NewPrunableURL(path))
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// ByteCountString mirrors ByteCountFormatter().string(fromByteCount:).
func ByteCountString(byteCount int64) string {
	return fsutil.ByteCountString(byteCount)
}
