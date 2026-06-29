// Package fsutil provides Go-native filesystem, path-attribute, and small
// formatting helpers that replace the Foundation usage (NSURL resource values,
// NSFileManager, NSByteCountFormatter, NSUUID, the XAttr package) weave
// inherited from its tart Swift port. Paths are plain strings; callers use
// os/path/filepath directly for the trivial operations and these helpers for
// the ones with non-obvious Foundation equivalents.
//
// The implementation is split by concern: stat.go (existence, times, sizes,
// free space), xattr.go (extended attributes), copy.go (recursive copy),
// clone.go (APFS copy-on-write clone), path.go (tilde expansion), bytes.go
// (byte-count formatting), and uuid.go (UUID generation).
//go:build darwin

package fsutil
