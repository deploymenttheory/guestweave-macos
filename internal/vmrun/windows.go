//go:build darwin

// Windows-guest run path for `weave run <name>` when the VM's OS is Windows.
// Windows boots on the QEMU backend (a headless subprocess exposing a VNC
// server), so this avoids the Virtualization.framework + AppKit machinery of the
// main run path and instead drives the backend lifecycle directly, reusing the
// existing VNC viewer plumbing (the same vnc:// URL handling driveVM uses).

package vmrun

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/deploymenttheory/guestweave/internal/backend"
	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	weavelock "github.com/deploymenttheory/guestweave/internal/lock"
	"github.com/deploymenttheory/guestweave/internal/objcutil"
	"github.com/deploymenttheory/guestweave/internal/qemu"
	"github.com/deploymenttheory/guestweave/internal/screenviewer"
	"github.com/deploymenttheory/guestweave/internal/ui"
	"github.com/deploymenttheory/guestweave/internal/unattended"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"
	"github.com/deploymenttheory/guestweave/internal/vmdirectory"
	weavevnc "github.com/deploymenttheory/guestweave/internal/vnc"
	"github.com/deploymenttheory/guestweave/internal/winimage"
)

// windowsStopTimeout bounds the graceful ACPI shutdown before QEMU is killed.
const windowsStopTimeout = 30 * time.Second

// runWindows boots a Windows guest on the QEMU backend and blocks until it
// stops (or the operator interrupts via SIGINT / `weave stop`).
func (c *Session) runWindows(vmDir *vmdirectory.VMDirectory, vmConfig *vmconfig.VMConfig) error {
	if vmConfig.Windows == nil {
		return weaveerrors.ErrGeneric("VM %q is missing its Windows configuration", c.Name)
	}

	// Re-validate the install ISO's architecture at boot: the create flow only
	// produces ARM64 media, but config.json can be hand-edited and the cached
	// ISO can be swapped, so never boot non-ARM64 install media.
	if vmConfig.Windows.InstallISO != "" {
		if err := winimage.RequireARM64ISO(vmConfig.Windows.InstallISO); err != nil {
			return weaveerrors.ErrGeneric("%s", err.Error())
		}
	}

	conf, err := weaveconfig.NewConfig()
	if err != nil {
		return err
	}

	// Serialize against concurrent runs while we acquire the per-VM lock.
	storageLock, err := weavelock.NewFileLock(conf.WeaveHomeDir)
	if err != nil {
		return err
	}
	defer storageLock.Close()
	if err := storageLock.Lock(); err != nil {
		return err
	}

	// Lock config.json (PID lock) so `weave list`/`stop` see the VM as running.
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
	defer lock.Unlock() //nolint:errcheck
	if err := storageLock.Unlock(); err != nil {
		return err
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	// SIGINT (Ctrl-C and `weave stop`) requests a graceful shutdown.
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT)
	go func() {
		<-sigint
		fmt.Println("Stopping Windows VM...")
		cancelRun()
	}()

	b := qemu.New(conf.WeaveCacheDir)
	inst, err := b.Start(runCtx, vmDir, vmConfig, backend.StartOptions{
		Headless:    c.NoGraphics,
		VNCPassword: c.VNCPassword,
		InstallISO:  vmConfig.Windows.InstallISO,
	})
	if err != nil {
		return err
	}

	if endpoint, ok := inst.VNCEndpoint(); ok {
		c.presentWindowsVNC(runCtx, vmDir, endpoint)
		defer os.Remove(vmDir.VNCEndpointPath())
		// While installing, automate the UEFI boot steps over OCR/VNC so Windows
		// Setup starts without a human dismissing bootmgr's "Press any key" prompt
		// (which renders only on the display). Both setup modes share this; for
		// "unattend" the embedded autounattend.xml then drives Setup, for
		// "interactive" the user takes over the live Setup UI afterwards.
		if vmConfig.Windows.InstallISO != "" {
			go c.driveWindowsUEFISetup(runCtx, endpoint)
		}
	}

	// Wait for the guest to stop, or for an interrupt to request shutdown.
	if err := inst.Wait(runCtx); err != nil && runCtx.Err() != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), windowsStopTimeout)
		defer cancel()
		if stopErr := inst.Stop(stopCtx); stopErr != nil {
			return stopErr
		}
	}
	return nil
}

// driveWindowsUEFISetup connects to the running VM's VNC server and runs the
// "windows-uefi" OCR preset, which presses past bootmgr's optical-boot prompt so
// Windows Setup / WinPE starts without a human at the console. Errors are
// non-fatal and only logged — the operator can always drive the prompt over VNC.
func (c *Session) driveWindowsUEFISetup(ctx context.Context, endpoint string) {
	cfg, err := unattended.LoadUnattendedConfig("windows-uefi")
	if err != nil {
		fmt.Fprintf(os.Stderr, "windows uefi automation: load preset: %v\n", err)
		return
	}
	commands, err := unattended.ParseBootCommands(cfg.BootCommands)
	if err != nil {
		fmt.Fprintf(os.Stderr, "windows uefi automation: parse preset: %v\n", err)
		return
	}
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "windows uefi automation: bad VNC endpoint %q: %v\n", endpoint, err)
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "windows uefi automation: bad VNC port %q: %v\n", portStr, err)
		return
	}

	// VNC (and its password, set best-effort over QMP) come up shortly after the
	// process starts; retry the dial briefly.
	var vnc *weavevnc.VNCClient
	for attempt := 0; attempt < 30; attempt++ {
		if ctx.Err() != nil {
			return
		}
		if vnc, err = weavevnc.DialVNC(ctx, host, port, c.VNCPassword); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	if vnc == nil {
		fmt.Fprintf(os.Stderr, "windows uefi automation: connect VNC %s: %v\n", endpoint, err)
		return
	}
	defer vnc.Close()

	if cfg.BootWait > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(cfg.BootWait) * time.Second):
		}
	}

	fmt.Println("Automating Windows UEFI boot (press-any-key) over OCR...")
	automation := unattended.NewAutomation(vnc, false, "")
	if err := automation.ExecuteAll(ctx, commands); err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "windows uefi automation: %v\n", err)
		}
		return
	}
	fmt.Println("Windows Setup reached.")
}

// presentWindowsVNC reuses weave's VNC viewer plumbing for the QEMU VNC server:
// it records the endpoint, opens the system viewer, and optionally starts the
// view-only MJPEG screen server — mirroring driveVM's vncImpl block.
func (c *Session) presentWindowsVNC(ctx context.Context, vmDir *vmdirectory.VMDirectory, endpoint string) {
	vncImpl := weavevnc.NewQEMUVNC(endpoint)
	vncURL, err := vncImpl.WaitForURL(ctx, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	ui.SetVNCURL(vncURL)
	_ = os.WriteFile(vmDir.VNCEndpointPath(), []byte(vncURL), 0o600)

	_, onCI := objcutil.EnvironmentValue("CI")
	if c.NoGraphics || onCI || c.ShowScreen {
		fmt.Printf("VNC server is running at %s\n", vncURL)
	} else {
		fmt.Printf("Opening %s...\n", vncURL)
		ui.OpenURL(vncURL)
	}

	if c.ShowScreen {
		if match := unattended.VNCURLPattern.FindStringSubmatch(vncURL); match != nil {
			if viewerPort, convErr := strconv.Atoi(match[3]); convErr == nil {
				if server, srvErr := screenviewer.NewScreenServer(); srvErr == nil {
					go screenviewer.StreamVNCToViewer(ctx, match[2], viewerPort, match[1], server)
					fmt.Printf("View-only screen: open %s in a browser to watch (no input reaches the VM).\n", server.URL())
					screenviewer.OpenInBrowser(server.URL())
				}
			}
		}
	}
}
