// NAT networking (VZNATNetworkDeviceAttachment), entitlement-free. Ports
// tart's NetworkShared. Attachment-only: there is no out-of-band lifecycle, so
// NAT NICs carry no engine.
//go:build darwin

package network

import (
	idvirt "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
)

// buildNAT constructs a NAT NIC.
func buildNAT(mac *idvirt.MACAddress) (NIC, error) {
	return NIC{Attachment: idvirt.NewNATNetworkDeviceAttachment(), MAC: mac}, nil
}
