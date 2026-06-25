// Package winimage acquires Windows 11 ARM64 installation media for the QEMU
// backend. It downloads the official multi-edition ARM64 ISO from Microsoft's
// software-download site, and optionally re-masters it with an
// autounattend.xml injected at the root for an unattended install.
//
// Two pathways:
//
//   - Plain:    download the official ISO → use directly.
//   - Unattend: download the official ISO → extract (hdiutil) → inject
//     autounattend.xml → re-master with pkg/iso.BuildWindowsUDF.
//
// Both ISOs are cached under <cacheDir>/iso/swdl/.
//
//go:build darwin

package winimage

import (
	"context"
	"fmt"
	"io"
)

const (
	// arm64VolumeID is the ISO volume label Microsoft uses for ARM64 media.
	arm64VolumeID = "CCCOMA_ARM64FRE"

	// isoFormatVersion changes the re-mastered ISO cache key whenever the
	// injection/mastering logic changes.
	isoFormatVersion = 2
)

// Options configures an Acquire call.
type Options struct {
	// Edition is the Windows edition label for display; defaults to
	// "Professional". The official multi-edition ISO contains all editions —
	// this field does not filter the download.
	Edition string
	// Language selects the ISO language. Accepts BCP-47 tags ("en-us") or
	// Microsoft's localised language names ("English (United States)").
	// Defaults to "English (United States)".
	Language string
	// CacheDir is the root for cached ISOs (required), typically
	// <WeaveCacheDir>/windows.
	CacheDir string
	// Progress, when non-nil, receives human-readable progress lines.
	Progress io.Writer
	// Unattend, when non-empty, is autounattend.xml content embedded at the
	// ISO root. The result is cached under a content-hash suffix so it never
	// collides with the plain ISO.
	Unattend []byte
}

// Result describes the acquired install media.
type Result struct {
	ISOPath  string // path to the bootable ARM64 ISO
	Build    int    // build number parsed from the ISO filename, or 0
	Release  string // version string from the ISO filename (e.g. "25H2")
	Edition  string // Windows edition label
	FileName string // ISO filename
}

func (o Options) withDefaults() Options {
	if o.Edition == "" {
		o.Edition = "Professional"
	}
	if o.Language == "" {
		o.Language = "English (United States)"
	}
	return o
}

// Acquire downloads (or reuses a cached) official Windows 11 ARM64 ISO and,
// when opts.Unattend is non-empty, re-masters it with the autounattend.xml
// injected at the ISO root.
func Acquire(ctx context.Context, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	if opts.CacheDir == "" {
		return nil, fmt.Errorf("winimage: CacheDir is required")
	}
	return acquireSWDL(ctx, opts)
}

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format, args...)
	}
}
