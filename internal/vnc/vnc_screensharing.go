// Port of tart's VNC/ScreenSharingVNC.swift: points a vnc:// URL at the
// VM's resolved IP, to be opened with macOS Screen Sharing.
//go:build darwin

package vnc

import (
	"context"

	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/macaddress"
)

// ScreenSharingVNC ports tart's ScreenSharingVNC class.
type ScreenSharingVNC struct {
	VMConfig *vmconfig.VMConfig
}

var _ VNC = (*ScreenSharingVNC)(nil)

func NewScreenSharingVNC(vmConfig *vmconfig.VMConfig) *ScreenSharingVNC {
	return &ScreenSharingVNC{VMConfig: vmConfig}
}

func (v *ScreenSharingVNC) WaitForURL(ctx context.Context, netBridged bool) (string, error) {
	vmMACAddress, ok := macaddress.NewMACAddress(v.VMConfig.MACAddress.String())
	if !ok {
		return "", weaveerrors.ErrGeneric("failed to parse VM's MAC address")
	}

	strategy := macaddress.IPResolutionStrategyDHCP
	if netBridged {
		strategy = macaddress.IPResolutionStrategyARP
	}
	ip, found, err := macaddress.ResolveIP(ctx, vmMACAddress, strategy, 60, "")
	if err != nil {
		return "", err
	}
	if !found {
		return "", IPNotFoundError{}
	}

	return "vnc://" + ip.String(), nil
}

func (v *ScreenSharingVNC) Stop() error {
	// Nothing to do.
	return nil
}
