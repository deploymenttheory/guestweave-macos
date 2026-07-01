// Bridged networking (VZBridgedNetworkDeviceAttachment): the NIC is bridged
// onto a host physical interface. Requires the com.apple.vm.networking
// entitlement or root; the entitlement failure surfaces when the VM starts.
// Attachment-only: no out-of-band lifecycle, so bridged NICs carry no engine.
//go:build darwin

package network

import (
	"strings"

	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/vmconfig"
)

// buildBridged constructs a bridged NIC, resolving the host interface by
// identifier or localized display name.
func buildBridged(nicConfig vmconfig.NICConfig, mac *idvirt.MACAddress) (NIC, error) {
	iface, found := FindBridgedInterface(nicConfig.BridgedInterface)
	if !found {
		return NIC{}, weaveerrors.ErrGeneric(
			"no bridge interfaces matched %q, available interfaces: %s",
			nicConfig.BridgedInterface, strings.Join(BridgeInterfaces(), ", "))
	}
	return NIC{
		Attachment: idvirt.NewBridgedNetworkDeviceAttachmentWithInterface(iface),
		MAC:        mac,
	}, nil
}

// FindBridgedInterface resolves a host bridged interface by identifier or
// localized display name. An empty name selects the first available interface.
func FindBridgedInterface(name string) (*idvirt.BridgedNetworkInterface, bool) {
	for _, iface := range idvirt.NetworkInterfaces() {
		if name == "" || iface.Identifier() == name || iface.LocalizedDisplayName() == name {
			return iface, true
		}
	}
	return nil, false
}

// BridgeInterfaces lists the available host bridged interfaces, each as
// "identifier (or \"Display Name\")", for error messages and `--net-bridged=list`.
func BridgeInterfaces() []string {
	var descriptions []string
	for _, iface := range idvirt.NetworkInterfaces() {
		description := iface.Identifier()
		if displayName := iface.LocalizedDisplayName(); displayName != "" {
			description += " (or \"" + displayName + "\")"
		}
		descriptions = append(descriptions, description)
	}
	return descriptions
}
