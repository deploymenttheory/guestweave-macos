// File metadata queries (replacing NSURL resource keys / NSFileManager):
// existence, access time, logical and on-disk size, and filesystem free space.
//go:build darwin

package fsutil

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Exists reports whether a file or directory exists at path. It mirrors
// NSFileManager.fileExists(atPath:).
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

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

// AvailableBytes returns the free space (bytes available to an unprivileged
// user) on the filesystem that contains path.
func AvailableBytes(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
