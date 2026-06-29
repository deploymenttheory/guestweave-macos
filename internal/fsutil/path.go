// Path helpers (replacing NSString path utilities).
//go:build darwin

package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

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
