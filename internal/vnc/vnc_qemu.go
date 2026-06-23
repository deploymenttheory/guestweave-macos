// QEMUVNC adapts the QEMU backend's built-in VNC server to weave's VNC
// interface, so the existing run-window plumbing (ui.SetVNCURL, the screen
// viewer, the .vnc-endpoint file) works unchanged for Windows guests. Unlike
// ScreenSharingVNC, the endpoint is known up front (QEMU binds it at launch),
// so WaitForURL returns immediately.
//go:build darwin

package vnc

import "context"

// QEMUVNC reports a fixed vnc:// URL for a QEMU guest's VNC server.
type QEMUVNC struct {
	hostPort string // "127.0.0.1:5900"
}

var _ VNC = (*QEMUVNC)(nil)

// NewQEMUVNC returns a VNC reporting vnc://<hostPort>.
func NewQEMUVNC(hostPort string) *QEMUVNC {
	return &QEMUVNC{hostPort: hostPort}
}

func (v *QEMUVNC) WaitForURL(_ context.Context, _ bool) (string, error) {
	return "vnc://" + v.hostPort, nil
}

func (v *QEMUVNC) Stop() error { return nil }
