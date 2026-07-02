// Helpers for reaching a running guest: resolving a VM's IP address, used by
// the HTTP API's exec, ssh and ip handlers.
//go:build darwin

package vmservice

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
	mac, ok := macaddress.NewMACAddress(vmConfig.MACAddress.String())
	if !ok {
		return "", false, weaveerrors.ErrGeneric("failed to parse VM's MAC address")
	}
	ip, found, err := macaddress.ResolveIP(ctx, mac, resolver, wait, vmDir.ControlSocketURL())
	if err != nil || !found {
		return "", false, err
	}
	return ip.String(), true, nil
}
