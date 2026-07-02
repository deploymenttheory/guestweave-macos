// Port of lume's Commands/IPSW.swift: print the download URL of the latest
// supported macOS restore image, for manual download or use with
// "create --from-ipsw".
//go:build darwin

package command

import (
	"context"
	"fmt"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	"github.com/deploymenttheory/guestweave/internal/terminal"
)

// IPSWCommand ports the ipsw command.
type IPSWCommand struct{}

func (c *IPSWCommand) Run(ctx context.Context) error {
	spinner := terminal.NewSpinner("Looking up the latest supported IPSW")
	spinner.Start()
	image, err := FetchLatestSupportedRestoreImage(ctx)
	if err != nil {
		spinner.Fail("Failed to look up the latest supported IPSW")
		return err
	}
	spinner.Stop()
	fmt.Println(absoluteURLString(image.URL()))
	return nil
}

// absoluteURLString returns the absolute string of an NSURL handed back as an
// untyped object, or "" when it is not a URL.
func absoluteURLString(o obj.Object) string {
	u, ok := obj.As(o, "NSURL", foundation.URLFromID)
	if !ok {
		return ""
	}
	return u.AbsoluteString()
}
