// Package serial runs the weave guest agent over a virtio serial console instead
// of an SSH stdio channel. It is how the *resident* agent (a macOS LaunchAgent /
// Linux systemd user service, running inside the logged-in GUI session) talks to
// the host: the host attaches a VZFileHandleSerialPortAttachment to the VM and
// the guest opens the corresponding /dev node.
//
// On connect the agent puts the device in raw mode (so the binary framing in
// guestagent/proto is not mangled by the terminal line discipline) and runs the
// standard serve loop. The host drives the hello handshake once it has installed
// and started this resident agent (see client.SerialConn), so no unsolicited
// banner is needed.
package serial

import (
	"github.com/deploymenttheory/weave/internal/guestweaveagent/agent"
	"github.com/deploymenttheory/weave/internal/guestweaveagent/proto"
)

// Serve discovers (when dev is empty) and opens the virtio serial device, puts it
// in raw mode, then runs the agent serve loop over it until the host closes the
// channel.
func Serve(registry *agent.Registry, dev string) error {
	f, err := openDevice(dev)
	if err != nil {
		return err
	}
	defer f.Close()

	in := proto.NewBufferedReader(f)
	out := proto.NewBufferedWriter(f)
	return agent.Serve(in, out, registry)
}
