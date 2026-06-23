//go:build darwin

package qemu

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/deploymenttheory/weave/internal/backend"
	"github.com/deploymenttheory/weave/internal/vmconfig"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
)

const (
	vncBasePort = 5900
	vncMaxScan  = 100 // VNC display numbers 0..99 → ports 5900..5999
)

// Backend launches Windows 11 ARM64 guests on qemu-system-aarch64. It satisfies
// backend.Backend.
type Backend struct {
	// CacheDir is the weave cache root, used to resolve / auto-download the
	// QEMU toolchain.
	CacheDir string
}

var _ backend.Backend = (*Backend)(nil)

// New returns a QEMU backend resolving its toolchain under cacheDir.
func New(cacheDir string) *Backend { return &Backend{CacheDir: cacheDir} }

// Start boots the Windows guest described by cfg and returns a handle.
func (b *Backend) Start(ctx context.Context, vmDir *vmdirectory.VMDirectory, cfg *vmconfig.VMConfig, opts backend.StartOptions) (backend.Instance, error) {
	tc, err := ResolveToolchain(b.CacheDir)
	if err != nil {
		return nil, err
	}

	if err := ensureEFIVars(tc.FirmwareVarsTemplate, vmDir.EFIVarsURL()); err != nil {
		return nil, err
	}

	display, err := findFreeVNCDisplay()
	if err != nil {
		return nil, err
	}

	spec := Spec{
		Toolchain:      tc,
		Config:         cfg,
		VMDir:          vmDir,
		InstallISO:     opts.InstallISO,
		VNCDisplay:     display,
		VNCPasswordSet: opts.VNCPassword != "",
	}
	args := BuildArgs(spec)

	logPath := filepath.Join(vmDir.BaseURL, "qemu.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("qemu: open log: %w", err)
	}

	// QMP socket is recreated each run.
	_ = os.Remove(vmDir.QMPSocketURL())

	cmd := exec.Command(tc.SystemAARCH64, args...) //nolint:gosec // args built from validated config
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("qemu: start %s: %w (see %s)", tc.SystemAARCH64, err, logPath)
	}

	inst := &instance{
		cmd:         cmd,
		logFile:     logFile,
		logPath:     logPath,
		qmpPath:     vmDir.QMPSocketURL(),
		vncHostPort: fmt.Sprintf("127.0.0.1:%d", vncBasePort+display),
		exited:      make(chan struct{}),
	}
	go inst.reap()

	// Connect QMP and apply the VNC password (best effort within a window).
	q, err := dialQMP(inst.qmpPath, time.Now().Add(15*time.Second))
	if err != nil {
		// QEMU may still be booting headlessly; surface but don't kill — the
		// process is running and visible via VNC. Stop falls back to a kill.
		return inst, nil //nolint:nilerr // QMP is best-effort; VM is usable
	}
	if opts.VNCPassword != "" {
		if err := q.setVNCPassword(opts.VNCPassword); err != nil {
			q.close()
			_ = inst.kill()
			return nil, err
		}
	}
	q.close()

	return inst, nil
}

// instance is a running QEMU guest.
type instance struct {
	cmd         *exec.Cmd
	logFile     *os.File
	logPath     string
	qmpPath     string
	vncHostPort string

	exited  chan struct{}
	waitErr error
}

var _ backend.Instance = (*instance)(nil)

// reap waits for the process to exit and closes exited.
func (i *instance) reap() {
	i.waitErr = i.cmd.Wait()
	i.logFile.Close()
	close(i.exited)
}

// Wait blocks until the guest stops or ctx is cancelled.
func (i *instance) Wait(ctx context.Context) error {
	select {
	case <-i.exited:
		return i.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop requests a graceful ACPI shutdown over QMP, then waits for exit until
// ctx is done, escalating to a kill.
func (i *instance) Stop(ctx context.Context) error {
	select {
	case <-i.exited:
		return nil
	default:
	}

	if q, err := dialQMP(i.qmpPath, time.Now().Add(3*time.Second)); err == nil {
		_ = q.powerdown()
		q.close()
	}

	select {
	case <-i.exited:
		return nil
	case <-ctx.Done():
		return i.kill()
	}
}

// kill force-terminates the process and waits for the reaper.
func (i *instance) kill() error {
	if i.cmd.Process != nil {
		_ = i.cmd.Process.Kill()
	}
	<-i.exited
	return nil
}

// VNCEndpoint returns the QEMU VNC server's host:port.
func (i *instance) VNCEndpoint() (string, bool) {
	return i.vncHostPort, true
}

// ensureEFIVars makes a writable per-VM copy of the firmware vars template the
// first time the VM runs.
func ensureEFIVars(template, dest string) error {
	if fileExists(dest) {
		return nil
	}
	if template == "" {
		return fmt.Errorf("qemu: no UEFI vars template to seed %s", dest)
	}
	return copyFile(template, dest)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// findFreeVNCDisplay returns the first VNC display number whose loopback port is
// free, so concurrent VMs don't collide.
func findFreeVNCDisplay() (int, error) {
	for n := range vncMaxScan {
		addr := fmt.Sprintf("127.0.0.1:%d", vncBasePort+n)
		l, err := net.Listen("tcp", addr)
		if err == nil {
			l.Close()
			return n, nil
		}
	}
	return 0, fmt.Errorf("qemu: no free VNC display in %d..%d", vncBasePort, vncBasePort+vncMaxScan-1)
}
