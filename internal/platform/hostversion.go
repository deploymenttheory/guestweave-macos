// Host macOS version checks shared across packages.
//go:build darwin

package platform

import (
	"strconv"
	"strings"
	"syscall"
)

// MacOSAtLeast mirrors Swift's #available(macOS N, *) checks, reading the host
// product version from sysctl(kern.osproductversion).
func MacOSAtLeast(major int) bool {
	version, err := syscall.Sysctl("kern.osproductversion")
	if err != nil {
		return false
	}
	majorStr, _, _ := strings.Cut(version, ".")
	got, err := strconv.Atoi(majorStr)
	if err != nil {
		return false
	}
	return got >= major
}
