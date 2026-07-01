// Port of tart's URL+AccessDate.swift, as path-based helpers over fsutil.
//go:build darwin

package prune

import (
	"time"

	"github.com/deploymenttheory/guestweave/internal/fsutil"
)

// AccessDate ports URL.accessDate(): a file's last access time.
func AccessDate(path string) (time.Time, error) {
	return fsutil.AccessTime(path)
}

// UpdateAccessDate ports URL.updateAccessDate(_:): sets the access time,
// preserving the prior access time as the new modification time.
func UpdateAccessDate(path string, accessDate time.Time) error {
	return fsutil.SetAccessTime(path, accessDate)
}
