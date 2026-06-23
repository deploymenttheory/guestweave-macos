// Package backend defines the virtualization-engine seam. weave's original
// engine is Apple's Virtualization.framework (the VZ path in internal/vm),
// which boots macOS and Linux guests. Windows guests cannot run on VZ, so they
// run on a second engine — QEMU (internal/qemu) — which implements Backend.
//
// The seam is deliberately small: the create/run commands, the config model and
// the on-disk layout are shared, and only the launch + lifecycle differ per
// engine. Dispatch is by guest OS (vmconfig.VMConfig.OS).
//go:build darwin

package backend

import (
	"context"

	"github.com/deploymenttheory/weave/internal/vmconfig"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
)

// StartOptions are engine-agnostic run-time options resolved from CLI flags.
type StartOptions struct {
	// Headless suppresses any host GUI window; the guest is reachable via VNC
	// only.
	Headless bool
	// VNCPassword optionally secures the backend's VNC server. Empty disables
	// authentication.
	VNCPassword string
	// InstallISO, when non-empty, is attached as removable boot media for this
	// run (Windows install / first boot). Empty boots the installed system
	// disk.
	InstallISO string
}

// Instance is a handle to a running guest.
type Instance interface {
	// Wait blocks until the guest stops on its own or ctx is cancelled. A
	// cancelled ctx is the caller's signal to Stop.
	Wait(ctx context.Context) error
	// Stop requests a graceful shutdown and returns once the guest has stopped
	// or ctx is cancelled.
	Stop(ctx context.Context) error
	// VNCEndpoint returns the "host:port" of the backend's VNC server and true
	// when one is exposed, so the existing weave VNC viewer can attach.
	VNCEndpoint() (string, bool)
}

// Backend launches guests on a particular virtualization engine.
type Backend interface {
	// Start boots the VM described by cfg, whose files live in vmDir, and
	// returns a handle to the running guest.
	Start(ctx context.Context, vmDir *vmdirectory.VMDirectory, cfg *vmconfig.VMConfig, opts StartOptions) (Instance, error)
}
