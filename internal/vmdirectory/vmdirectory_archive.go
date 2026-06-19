// Port of tart's VMDirectory+Archive.swift: export/import of a VM directory
// using Apple's archive format with LZFSE compression, driving /usr/bin/aa
// (which produces and consumes the identical .aar format) via os/exec.
//go:build darwin

package vmdirectory

import (
	"bytes"
	"errors"
	"os/exec"

	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
)

// ExportToArchive ports VMDirectory.exportToArchive(path:).
func (d *VMDirectory) ExportToArchive(path string) error {
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

// ImportFromArchive ports VMDirectory.importFromArchive(path:).
func (d *VMDirectory) ImportFromArchive(path string) error {
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
