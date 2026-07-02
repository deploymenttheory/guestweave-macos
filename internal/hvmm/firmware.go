//go:build darwin

package hvmm

import (
	"os"
	"path/filepath"
	"runtime"

	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
)

// stockFirmware is the Homebrew-installed edk2 build. It uses the ARM *virtual*
// generic timer, which Apple's in-kernel vGIC does NOT deliver to a guest running
// at EL2 (Apple Feedback FB21649319) — so booting it on this backend stalls at the
// UEFI boot manager waiting for a timer tick. It remains the last-resort fallback.
const stockFirmware = "/opt/homebrew/share/qemu/edk2-aarch64-code.fd"

// physTimerFirmware is the name of the CI-built firmware that uses the *physical*
// generic timer (the FB21649319 workaround); the Apple vGIC delivers the physical
// timer PPI at EL2. It is produced by .github/workflows/build-el2-firmware.yml and
// committed under internal/hvmm/firmware/.
const physTimerFirmware = "edk2-aarch64-code-phytimer.fd"

// ResolveFirmware picks the firmware image to boot, preferring the physical-timer
// build that works at EL2 (FB21649319). Order: explicit path → GUESTWEAVE_HVMM_FIRMWARE
// → the committed physical-timer firmware (resolved relative to this source tree)
// → the stock virtual-timer firmware as a fallback.
func ResolveFirmware(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p := weaveconfig.HVMMFirmware(); p != "" {
		return p
	}
	if _, src, _, ok := runtime.Caller(0); ok {
		p := filepath.Join(filepath.Dir(src), "firmware", physTimerFirmware)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return stockFirmware
}
