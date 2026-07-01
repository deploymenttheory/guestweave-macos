// Command weave-guestd is the weave guest agent. It multiplexes feature modules
// (clipboard today; more to come) using the framing in guestagent/proto and the
// dispatch loop in guestagent/agent.
//
// It runs in one of two transport modes:
//
//   - "serve-serial [dev]" — the resident mode. weave-guestd runs inside the
//     guest's GUI/login session (a macOS LaunchAgent / Linux systemd user
//     service) and serves over a virtio serial console, so its clipboard backend
//     reaches the desktop pasteboard natively. This is how weave drives the
//     clipboard.
//   - no arguments — legacy stdio mode (host launches it over an SSH channel).
package main

import (
	"os"

	"github.com/deploymenttheory/guestweave/internal/guestweaveagent/agent"
	clipguest "github.com/deploymenttheory/guestweave/internal/guestweaveagent/modules/clipboard"
	"github.com/deploymenttheory/guestweave/internal/guestweaveagent/proto"
	"github.com/deploymenttheory/guestweave/internal/guestweaveagent/serial"
)

func main() {
	registry := agent.NewRegistry(
		clipguest.New(),
		// Future modules register here (file transfer, exec, telemetry, …).
	)

	if len(os.Args) > 1 && os.Args[1] == "serve-serial" {
		dev := ""
		if len(os.Args) > 2 {
			dev = os.Args[2]
		}
		if err := serial.Serve(registry, dev); err != nil {
			os.Stderr.WriteString("weave-guestd: " + err.Error() + "\n")
			os.Exit(1)
		}
		return
	}

	in := proto.NewBufferedReader(os.Stdin)
	out := proto.NewBufferedWriter(os.Stdout)
	if err := agent.Serve(in, out, registry); err != nil {
		os.Stderr.WriteString("weave-guestd: " + err.Error() + "\n")
		os.Exit(1)
	}
}
