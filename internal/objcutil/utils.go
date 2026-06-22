// Port of tart's Utils.swift, plus the Go↔Foundation bridge helpers shared by
// every file in this package.
//go:build darwin

package objcutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/errkit"
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/rt"
)

var (
	SelCount         = purego.RegisterName("count")
	SelObjectAtIndex = purego.RegisterName("objectAtIndex:")

	selLength = purego.RegisterName("length")
	selBytes  = purego.RegisterName("bytes")
)

// NSStr converts a Go string to an idiomatic Foundation String. Callers pass
// .ID() where an objc.ID is wanted, or .Unwrap() where a raw *NSString is.
func NSStr(s string) *foundation.String {
	return foundation.NewStringWithUTF8String(s)
}

// GoStr converts an NSString — raw or idiomatic, identified by its objc.ID — to
// a Go string. Callers pass the value's .Ptr() (raw) or .ID() (idiomatic).
func GoStr(id purego.ID) string {
	if id == 0 {
		return ""
	}
	return purego.GoString(id)
}

// EnvironmentValue mirrors Swift's ProcessInfo.processInfo.environment[name].
func EnvironmentValue(name string) (string, bool) {
	return os.LookupEnv(name)
}

// AllocClass sends +alloc to the named class, for use with the generated
// Init* instance methods.
func AllocClass(className string) purego.ID {
	return purego.Send[purego.ID](purego.ID(purego.GetClass(className)), purego.RegisterName("alloc"))
}

// NSDataToBytes copies an NSData's contents — raw or idiomatic, identified by
// its objc.ID — into a Go byte slice. The length/bytes are read with direct
// message sends so no wrapper (and thus no releasing finalizer) is created over
// a borrowed object.
func NSDataToBytes(id purego.ID) []byte {
	if id == 0 {
		return nil
	}
	length := purego.Send[uint](id, selLength)
	if length == 0 {
		return nil
	}
	bytes := purego.Send[unsafe.Pointer](id, selBytes)
	return append([]byte(nil), unsafe.Slice((*byte)(bytes), length)...)
}

// BytesToNSData copies a Go byte slice into a new idiomatic Foundation Data.
func BytesToNSData(b []byte) *foundation.Data {
	return foundation.DataFromID(rt.BytesToNSData(b))
}

// IsURLError reports whether err is an NSURLErrorDomain error — the Go
// equivalent of Swift's `error is URLError` checks.
func IsURLError(err error) bool {
	var e *errkit.Error
	return errors.As(err, &e) && e.Domain() == "NSURLErrorDomain"
}

// RetryOnURLError ports the Retry package usage: retry fn up to maxAttempts
// times, but only when the failure is a URL (network) error.
func RetryOnURLError(maxAttempts int, fn func() error) error {
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if !IsURLError(err) {
			return err
		}
		fmt.Printf("Error: %v\nAttempting to re-try...\n", err)
	}
	return err
}

// ResolveBinaryPath ports tart's ResolveBinaryPath: it walks $PATH and returns
// the path of the first entry containing an executable file called name, or ""
// when not found.
func ResolveBinaryPath(name string) string {
	path, ok := os.LookupEnv("PATH")
	if !ok {
		return ""
	}
	for _, dir := range strings.Split(path, ":") {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// TextPreview ports Data.asTextPreview(limit:).
func TextPreview(data []byte) string {
	const limit = 1000
	if len(data) <= limit {
		return string(data)
	}
	return string(data[:limit]) + "..."
}

// ExpandTilde ports NSString.expandingTildeInPath for the common ~ and ~/ forms.
func ExpandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// NSURLFromPath builds a file URL from a Go path string. Callers pass .ID()
// where an objc.ID is wanted, or .Unwrap() for a raw *NSURL.
func NSURLFromPath(path string) *foundation.URL {
	return foundation.NewURLFileURLWithPath(path)
}

// AbsoluteURLString returns the absolute string of an NSURL handed back as an
// untyped object (e.g. RestoreImage.URL()), or "" when it is not a URL.
func AbsoluteURLString(o obj.Object) string {
	u, ok := obj.As(o, "NSURL", foundation.URLFromID)
	if !ok {
		return ""
	}
	return u.AbsoluteString()
}
