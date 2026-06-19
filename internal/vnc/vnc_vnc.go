// Port of tart's VNC/VNC.swift.
//go:build darwin

package vnc

import (
	"context"
)

// IPNotFoundError ports Run.swift's IPNotFound error.
type IPNotFoundError struct{}

func (IPNotFoundError) Error() string { return "IP not found" }

// VNC ports tart's VNC protocol. WaitForURL returns the vnc:// URL as a string.
type VNC interface {
	WaitForURL(ctx context.Context, netBridged bool) (string, error)
	Stop() error
}
