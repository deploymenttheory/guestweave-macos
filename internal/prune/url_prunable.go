// Port of tart's URL+Prunable.swift, adapting a plain file path to Prunable.
// File sizes, access dates, and the deduplicated-bytes xattr go through fsutil.
//go:build darwin

package prune

import (
	"os"
	"strconv"
	"time"

	"github.com/deploymenttheory/guestweave/internal/fsutil"
)

const deduplicatedBytesXattr = "run.weave.deduplicated-bytes"

// PrunableURL adapts a plain file path to the Prunable interface.
type PrunableURL struct {
	path string
}

var _ Prunable = (*PrunableURL)(nil)

func NewPrunableURL(path string) *PrunableURL {
	return &PrunableURL{path: path}
}

func (p *PrunableURL) Path() string { return p.path }

func (p *PrunableURL) Delete() error {
	return os.RemoveAll(p.path)
}

func (p *PrunableURL) AccessDate() (time.Time, error) {
	return fsutil.AccessTime(p.path)
}

func (p *PrunableURL) AllocatedSizeBytes() (int, error) {
	n, err := fsutil.AllocatedSizeBytes(p.path)
	return int(n), err
}

func (p *PrunableURL) SizeBytes() (int, error) {
	n, err := fsutil.SizeBytes(p.path)
	return int(n), err
}

// DeduplicatedSizeBytes ports URL.deduplicatedSizeBytes(). The Foundation
// original gated this on the mayShareFileContent resource value (an APFS
// clone-share check) which has no portable os/syscall equivalent, so the
// recorded count is reported unconditionally — valid while the clone origin is
// intact, an over-estimate only after the file diverges.
func (p *PrunableURL) DeduplicatedSizeBytes() (int, error) {
	return int(p.DeduplicatedBytes()), nil
}

// SetDeduplicatedBytes records the deduplicated byte count as an xattr.
func (p *PrunableURL) SetDeduplicatedBytes(size uint64) {
	if err := fsutil.SetXattr(p.path, deduplicatedBytesXattr, []byte(strconv.FormatUint(size, 10))); err != nil {
		panic(err)
	}
}

// DeduplicatedBytes returns the recorded deduplicated byte count, or 0 on any
// failure.
func (p *PrunableURL) DeduplicatedBytes() uint64 {
	data, err := fsutil.GetXattr(p.path, deduplicatedBytesXattr)
	if err != nil || len(data) == 0 {
		return 0
	}
	value, err := strconv.ParseUint(string(data), 10, 64)
	if err != nil {
		return 0
	}
	return value
}
