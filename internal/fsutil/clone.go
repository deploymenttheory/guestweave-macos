// APFS copy-on-write cloning (clonefile(2)) with a full-copy fallback.
//go:build darwin

package fsutil

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// CloneFile creates dst as an APFS copy-on-write clone of src when the
// filesystem supports it, falling back to a full byte copy otherwise (a
// non-APFS volume or a cross-volume copy). Cloning is near-instant and claims
// no extra space until either side is written, which is what makes keeping many
// VM disk snapshots practical. clonefile(2) requires dst to not exist, so any
// existing file is removed first.
func CloneFile(src, dst string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	switch err := unix.Clonefile(src, dst, 0); {
	case err == nil:
		return nil
	case errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EXDEV), errors.Is(err, unix.EINVAL):
		// Cloning unsupported here (e.g. not APFS, or across volumes) — fall back.
		info, statErr := os.Lstat(src)
		if statErr != nil {
			return statErr
		}
		return copyFile(src, dst, info)
	default:
		return err
	}
}
