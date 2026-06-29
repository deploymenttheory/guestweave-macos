// Byte-count formatting (replacing NSByteCountFormatter).
//go:build darwin

package fsutil

import "fmt"

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
