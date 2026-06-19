// nsProgressWrapper adapts a Foundation NSProgress (e.g. from VZMacOSInstaller)
// to logging.Progress. It lives in the vm package — the only consumer of a raw
// NSProgress — so the logging package stays framework-free.
//go:build darwin

package vm

import (
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
)

type nsProgressWrapper struct {
	inner *foundation.Progress
}

func (p *nsProgressWrapper) FractionCompleted() float64 { return p.inner.FractionCompleted() }
func (p *nsProgressWrapper) IsFinished() bool           { return p.inner.IsFinished() }
