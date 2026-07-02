// Helpers for reaching a running guest: resolving a VM's IP address, used by
// the HTTP API's exec, ssh and ip handlers.
//go:build darwin

package service

import (
	"context"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/macaddress"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"
	vmstorage "github.com/deploymenttheory/guestweave/internal/vm/storage"
)

// ResolveVMIP resolves a VM's IP using the given strategy, waiting up to wait
// seconds. It returns ("", false, nil) when no address is found.
func ResolveVMIP(
	ctx context.Context,
	name string,
	resolver macaddress.IPResolutionStrategy,
	wait uint16,
) (string, bool, error) {
	vmDir, err := vmstorage.OpenLocal(name)
	if err != nil {
		return "", false, err
	}
	vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return "", false, err
	}
	return resolveIPWithConfig(ctx, vmConfig, vmDir.ControlSocketURL(), resolver, wait)
}

// resolveIPWithConfig is the shared MAC-parse → ResolveIP core used with an
// already-loaded config (CollectVMDetails) or a freshly-opened one
// (ResolveVMIP).
func resolveIPWithConfig(
	ctx context.Context,
	vmConfig *vmconfig.VMConfig,
	controlSocket string,
	resolver macaddress.IPResolutionStrategy,
	wait uint16,
) (string, bool, error) {
	mac, ok := macaddress.NewMACAddress(vmConfig.MACAddress.String())
	if !ok {
		return "", false, weaveerrors.ErrGeneric("failed to parse VM's MAC address")
	}
	ip, found, err := macaddress.ResolveIP(ctx, mac, resolver, wait, controlSocket)
	if err != nil || !found {
		return "", false, err
	}
	return ip.String(), true, nil
}
