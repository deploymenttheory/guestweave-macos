// Thin entry point: the CLI lives in internal/cli and all features live in
// their own packages. Only the main-thread lock stays in this file — AppKit
// and Virtualization.framework dispatch their work to the main queue, so the
// main goroutine must be locked to the process's main thread before anything
// else runs.
//go:build darwin

package main

import (
	"runtime"

	"github.com/deploymenttheory/guestweave/internal/cli"
)

func init() {
	runtime.LockOSThread()
}

func main() {
	cli.Execute()
}
