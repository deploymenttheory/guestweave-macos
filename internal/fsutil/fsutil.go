// Package fsutil provides Go-native filesystem, path-attribute, and small
// formatting helpers that replace the Foundation usage (NSURL resource values,
// NSFileManager, NSByteCountFormatter, NSUUID, the XAttr package) weave
// inherited from its tart Swift port. Paths are plain strings; callers use
// os/path/filepath directly for the trivial operations and these helpers for
// the ones with non-obvious Foundation equivalents.
//go:build darwin

package fsutil

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Exists reports whether a file or directory exists at path. It mirrors
// NSFileManager.fileExists(atPath:).
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ── resource values (replacing NSURL resource keys) ──────────────────────────

// AccessTime returns a file's last access time — the contentAccessDate resource
// value (URL.accessDate()).
func AccessTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, fmt.Errorf("fsutil: no stat_t for %s", path)
	}
	return time.Unix(st.Atimespec.Sec, st.Atimespec.Nsec), nil
}

// SetAccessTime sets a file's access time, preserving the previous access time
// as the new modification time — matching URL.updateAccessDate(_:), which passed
// the prior access date as the mtime to utimes(2).
func SetAccessTime(path string, atime time.Time) error {
	prev, err := AccessTime(path)
	if err != nil {
		return err
	}
	times := []syscall.Timeval{
		{Sec: atime.Unix()},
		{Sec: prev.Unix()},
	}
	return syscall.Utimes(path, times)
}

// SizeBytes returns a file's logical size — the totalFileSize resource value.
func SizeBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// AllocatedSizeBytes returns a file's size on disk excluding holes — the
// totalFileAllocatedSize resource value (st_blocks is in 512-byte units).
func AllocatedSizeBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("fsutil: no stat_t for %s", path)
	}
	return int64(st.Blocks) * 512, nil
}

// ── extended attributes (replacing the XAttr package / NSURL) ─────────────────

// GetXattr reads an extended attribute. A missing attribute returns (nil, nil).
func GetXattr(path, name string) ([]byte, error) {
	size, err := xattr(syscall.SYS_GETXATTR, path, name, nil, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOATTR) {
			return nil, nil
		}
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	read, err := xattr(syscall.SYS_GETXATTR, path, name, &buf[0], uintptr(size))
	if err != nil {
		return nil, err
	}
	return buf[:read], nil
}

// SetXattr writes an extended attribute.
func SetXattr(path, name string, value []byte) error {
	var p *byte
	if len(value) > 0 {
		p = &value[0]
	}
	_, err := xattr(syscall.SYS_SETXATTR, path, name, p, uintptr(len(value)))
	return err
}

func xattr(trap uintptr, path, name string, value *byte, size uintptr) (int, error) {
	pathPtr, err := syscall.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	namePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return 0, err
	}
	n, _, errno := syscall.Syscall6(trap,
		uintptr(unsafe.Pointer(pathPtr)), uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(value)), size, 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

// ── file operations worth centralizing ───────────────────────────────────────

// CopyItem recursively copies src to dst (file or directory), mirroring
// NSFileManager.copyItem(at:to:).
// AvailableBytes returns the free space (bytes available to an unprivileged
// user) on the filesystem that contains path.
func AvailableBytes(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

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

func CopyItem(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dst, info)
	}
	return copyFile(src, dst, info)
}

func copyDir(src, dst string, info os.FileInfo) error {
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := CopyItem(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, info os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ── env / home (replacing objcutil) ──────────────────────────────────────────

// ExpandTilde expands a leading "~" to the current user's home directory,
// mirroring NSString.expandingTildeInPath.
func ExpandTilde(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// ── formatting / ids (replacing NSByteCountFormatter / NSUUID) ────────────────

// ByteCountString formats a byte count with decimal (SI) units, matching
// NSByteCountFormatter's .file count style.
func ByteCountString(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// UUID returns a random RFC 4122 version-4 UUID string, replacing
// NSUUID().uuidString (which is upper-cased).
func UUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}
