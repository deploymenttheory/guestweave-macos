// Helpers for reaching a running guest: resolving a VM's IP address, used by
// the HTTP API's exec, ssh and ip handlers.
//go:build darwin

package vmservice

import (
	"context"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/macaddress"
	"github.com/deploymenttheory/weave/internal/vmconfig"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// ResolveVMIP resolves a VM's IP using the given strategy, waiting up to wait
// seconds. It returns ("", false, nil) when no address is found.
func ResolveVMIP(
	ctx context.Context,
	name string,
	resolver macaddress.IPResolutionStrategy,
	wait uint16,
) (string, bool, error) {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return "", false, err
	}
	vmDir, err := storage.Open(name)
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
