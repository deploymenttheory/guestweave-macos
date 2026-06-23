// Package winimage acquires Windows 11 ARM64 installation media for the QEMU
// backend. It resolves a feature-release name (e.g. "24H2") to an ARM64 install
// ESD via Microsoft's Media Creation Tool catalog (the winmediafoundry SDK),
// downloads the ESD (verified against its catalog SHA-1), and assembles a
// bootable UEFI ISO with the SDK's pure-Go builder. Both the ESD and the ISO
// are cached under <cache>/windows so repeat creates are cheap.
//
// Only ARM64 is supported: Windows 11 ARM64 runs under QEMU + HVF on Apple
// Silicon at near-native speed, which is why it is the chosen guest arch.
//go:build darwin

package winimage

import (
	"context"
	"crypto/sha1" //nolint:gosec // SHA-1 is the catalog-published digest, not a security control
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/deploymenttheory/go-sdk-winmediafoundry/esd"
	esdapi "github.com/deploymenttheory/go-sdk-winmediafoundry/esd/api/esd"
	esdconst "github.com/deploymenttheory/go-sdk-winmediafoundry/esd/constants"
	"github.com/deploymenttheory/go-sdk-winmediafoundry/pkg/builder"
)

const (
	// catalogArch is the architecture string the MCT catalog uses for ARM64.
	catalogArch = "ARM64"
	// arm64VolumeID is the ISO volume label Microsoft uses for ARM64 media.
	arm64VolumeID = "CCCOMA_ARM64FRE"

	defaultEdition  = "Professional"
	defaultLanguage = "en-us"
)

// Options configures an Acquire call.
type Options struct {
	// Release is the Windows 11 feature release, e.g. "24H2" (required).
	Release string
	// Edition is the Windows edition; defaults to "Professional".
	Edition string
	// Language is the BCP-47 language tag; defaults to "en-us".
	Language string
	// CacheDir is the root for cached ESDs and ISOs (required), typically
	// <WeaveCacheDir>/windows.
	CacheDir string
	// Progress, when non-nil, receives human-readable progress lines.
	Progress io.Writer
}

// Result describes acquired install media.
type Result struct {
	ISOPath  string // bootable ARM64 install ISO
	ESDPath  string // the downloaded source ESD
	Build    int    // resolved base build number, e.g. 26100
	Release  string // the requested release, e.g. "24H2"
	Edition  string // the resolved edition
	FileName string // the ESD filename
}

// Acquire resolves, downloads and builds a bootable Windows 11 ARM64 install
// ISO for the given release. ESD and ISO are cached and reused.
func (o Options) withDefaults() Options {
	if o.Edition == "" {
		o.Edition = defaultEdition
	}
	if o.Language == "" {
		o.Language = defaultLanguage
	}
	return o
}

// Acquire performs the full release → ESD → bootable ISO pipeline.
func Acquire(ctx context.Context, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	if opts.Release == "" {
		return nil, fmt.Errorf("winimage: Release is required")
	}
	if opts.CacheDir == "" {
		return nil, fmt.Errorf("winimage: CacheDir is required")
	}

	build, ok := esdconst.ReleaseBuild(esdconst.Release(opts.Release))
	if !ok {
		return nil, fmt.Errorf("winimage: unknown Windows 11 release %q (known: %v)", opts.Release, esdconst.Releases())
	}

	img, err := resolve(ctx, build, opts)
	if err != nil {
		return nil, err
	}

	esdDir := filepath.Join(opts.CacheDir, "esd")
	isoDir := filepath.Join(opts.CacheDir, "iso")
	if err := os.MkdirAll(esdDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(isoDir, 0o755); err != nil {
		return nil, err
	}

	esdPath := filepath.Join(esdDir, img.FileName)
	if err := ensureESD(ctx, img, esdPath, opts.Progress); err != nil {
		return nil, err
	}

	isoName := fmt.Sprintf("%d-arm64-%s.iso", build, strings.ToLower(opts.Edition))
	isoPath := filepath.Join(isoDir, isoName)
	if err := ensureISO(esdPath, isoPath, opts.CacheDir, opts.Progress); err != nil {
		return nil, err
	}

	return &Result{
		ISOPath:  isoPath,
		ESDPath:  esdPath,
		Build:    build,
		Release:  opts.Release,
		Edition:  opts.Edition,
		FileName: img.FileName,
	}, nil
}

// resolve fetches the MCT catalog and selects the ARM64 ESD for the build.
func resolve(ctx context.Context, build int, opts Options) (esdapi.ESDImage, error) {
	client, err := esd.NewClient()
	if err != nil {
		return esdapi.ESDImage{}, fmt.Errorf("winimage: esd client: %w", err)
	}
	cat, _, err := client.Catalog(ctx, esdapi.WithProduct(esdapi.Windows11))
	if err != nil {
		return esdapi.ESDImage{}, fmt.Errorf("winimage: fetch catalog: %w", err)
	}

	matches := cat.FilterBuildMajor(build, opts.Edition, catalogArch, opts.Language)
	if len(matches) == 0 {
		// Distinguish "release not in catalog" (the common case: MCT carries
		// only the current GA release) from a bad edition/language.
		present := nonZero(cat.BuildMajors())
		return esdapi.ESDImage{}, fmt.Errorf(
			"winimage: no %s %s %s media for build %d in the Media Creation Tool catalog "+
				"(catalog currently carries ARM64 builds %v); the MCT catalog only lists the current GA release",
			catalogArch, opts.Edition, opts.Language, build, present)
	}
	return matches[0], nil
}

// ensureESD downloads img to esdPath unless a valid copy already exists.
func ensureESD(ctx context.Context, img esdapi.ESDImage, esdPath string, progress io.Writer) error {
	if info, err := os.Stat(esdPath); err == nil && info.Size() == img.SizeBytes {
		logf(progress, "Using cached ESD %s\n", filepath.Base(esdPath))
		return nil
	}
	logf(progress, "Downloading %s (%.2f GB)...\n", img.FileName, float64(img.SizeBytes)/1e9)
	if err := downloadVerified(ctx, img.URL, esdPath, img.SHA1, progress); err != nil {
		return fmt.Errorf("winimage: download ESD: %w", err)
	}
	return nil
}

// ensureISO builds isoPath from esdPath unless it already exists.
func ensureISO(esdPath, isoPath, cacheDir string, progress io.Writer) error {
	if info, err := os.Stat(isoPath); err == nil && info.Size() > 0 {
		logf(progress, "Using cached ISO %s\n", filepath.Base(isoPath))
		return nil
	}
	logf(progress, "Building bootable ARM64 ISO (this can take several minutes)...\n")

	work, err := os.MkdirTemp(cacheDir, "iso-build-")
	if err != nil {
		return fmt.Errorf("winimage: iso workdir: %w", err)
	}
	defer os.RemoveAll(work)

	tmpISO := isoPath + ".tmp"
	if err := builder.BuildISO(esdPath, tmpISO, builder.Options{
		VolumeID: arm64VolumeID,
		WorkDir:  work,
		Progress: progress, // live per-phase bar for the slow WIM rebuilds
	}); err != nil {
		_ = os.Remove(tmpISO)
		return fmt.Errorf("winimage: build ISO: %w", err)
	}
	if err := os.Rename(tmpISO, isoPath); err != nil {
		return fmt.Errorf("winimage: finalize ISO: %w", err)
	}
	logf(progress, "Built %s\n", filepath.Base(isoPath))
	return nil
}

// downloadVerified streams url to dest atomically, verifying the hex SHA-1.
func downloadVerified(ctx context.Context, url, dest, wantSHA1 string, progress io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s for %s", resp.Status, url)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".esd-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op once renamed
	}()

	h := sha1.New() //nolint:gosec // matching the catalog-published digest
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if wantSHA1 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, wantSHA1) {
			return fmt.Errorf("SHA-1 mismatch: got %s, want %s", got, wantSHA1)
		}
		logf(progress, "Verified SHA-1 %s\n", got)
	}
	return os.Rename(tmpName, dest)
}

func nonZero(in []int) []int {
	out := make([]int, 0, len(in))
	for _, v := range in {
		if v != 0 {
			out = append(out, v)
		}
	}
	return out
}

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format, args...)
	}
}
