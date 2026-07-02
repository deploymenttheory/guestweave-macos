// Package archive exports/imports a VM directory (.tvm) using Apple's
// archive format with LZFSE compression, driving /usr/bin/aa (which produces
// and consumes the identical .aar format) via os/exec. Port of tart's
// VMDirectory+Archive.swift.
//go:build darwin

package archive

import (
	"bytes"
	"errors"
	"os/exec"

	"github.com/deploymenttheory/guestweave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
)

// Export ports VMDirectory.exportToArchive(path:).
func Export(d *layout.VMDirectory, path string) error {
	if err := runAA([]string{
		"archive",
		"-d", d.BaseURL,
		"-o", path,
		"-a", "lzfse",
	}); err != nil {
		return weaveerrors.ErrExportFailed(err.Error())
	}
	return nil
}

// Import ports VMDirectory.importFromArchive(path:).
func Import(d *layout.VMDirectory, path string) error {
	if err := runAA([]string{
		"extract",
		"-d", d.BaseURL,
		"-i", path,
	}); err != nil {
		return weaveerrors.ErrImportFailed(err.Error())
	}
	return nil
}

func runAA(arguments []string) error {
	cmd := exec.Command("/usr/bin/aa", arguments...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return weaveerrors.ErrGeneric("aa failed with exit code %d: %s", exitErr.ExitCode(),
				diskimage.FirstNonEmptyLine(stderr.String()))
		}
		return err
	}
	return nil
}
