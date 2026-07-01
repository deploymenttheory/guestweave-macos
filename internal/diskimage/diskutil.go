// Port of tart's Diskutil.swift: shells out to diskutil(8) via os/exec and
// parses the --plist output with NSPropertyListSerialization (Swift:
// Process + PropertyListDecoder).
//go:build darwin

package diskimage

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	weaveerrors "github.com/deploymenttheory/guestweave/internal/errors"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/rt"
)

// nsData builds an idiomatic NSData from a Go byte slice.
func nsData(b []byte) *foundation.Data {
	return foundation.DataFromID(rt.BytesToNSData(b))
}

// dictUint64 reads an unsigned-integer value stored under key in dict.
func dictUint64(dict *foundation.Dictionary, key string) (uint64, bool) {
	num, ok := obj.As(dict.ObjectForKey(foundation.NewStringWithUTF8String(key)), "NSNumber", foundation.NumberFromID)
	if !ok {
		return 0, false
	}
	return num.UnsignedLongLongValue(), true
}

// dictDict reads a nested dictionary stored under key in dict.
func dictDict(dict *foundation.Dictionary, key string) (*foundation.Dictionary, bool) {
	return obj.As(dict.ObjectForKey(foundation.NewStringWithUTF8String(key)), "NSDictionary", foundation.DictionaryFromID)
}

// SizeInfo ports Diskutil.swift's SizeInfo ("Size Info" plist dictionary).
type SizeInfo struct {
	TotalBytes *uint64 // "Total Bytes"
}

// ImageInfo ports Diskutil.swift's ImageInfo ("diskutil image info" output).
type ImageInfo struct {
	SizeInfo *SizeInfo // "Size Info"
	Size     *uint64   // "Size"
}

// TotalBytes ports ImageInfo.totalBytes().
func (i *ImageInfo) TotalBytes() (int, error) {
	if i.SizeInfo != nil && i.SizeInfo.TotalBytes != nil {
		return int(*i.SizeInfo.TotalBytes), nil
	}
	if i.Size != nil {
		return int(*i.Size), nil
	}
	return 0, weaveerrors.ErrGeneric("Could not find size information in disk image info")
}

// DiskutilImageCreate ports Diskutil.imageCreate(diskURL:sizeGB:): creates a
// blank ASIF disk image.
func DiskutilImageCreate(diskPath string, sizeGB uint16) error {
	_, _, err := DiskutilRun([]string{
		"image", "create", "blank",
		"--format", "ASIF",
		"--size", fmt.Sprintf("%dG", sizeGB),
		"--volumeName", "Weave",
		diskPath,
	})
	if err != nil {
		return weaveerrors.ErrFailedToCreateDisk("Failed to create ASIF disk image: %v", err)
	}
	return nil
}

// DiskutilImageInfo ports Diskutil.imageInfo(_:).
func DiskutilImageInfo(diskPath string) (*ImageInfo, error) {
	stdoutData, _, err := DiskutilRun([]string{
		"image", "info", "--plist",
		diskPath,
	})
	if err != nil {
		return nil, err
	}

	// NSPropertyListSerialization writes the detected format to an out-parameter,
	// which the idiomatic binding surfaces as an extra return value (discarded).
	plistObj, _, err := foundation.PropertyListWithDataOptionsFormatError(nsData(stdoutData), 0)
	if err != nil || plistObj == nil {
		return nil, weaveerrors.ErrGeneric("Failed to parse \"diskutil image info --plist\" output: %v", err)
	}
	plist, ok := obj.As(plistObj, "NSDictionary", foundation.DictionaryFromID)
	if !ok {
		return nil, weaveerrors.ErrGeneric("Unexpected \"diskutil image info --plist\" output shape")
	}

	info := &ImageInfo{}
	if size, ok := dictUint64(plist, "Size"); ok {
		info.Size = &size
	}
	if sizeInfo, ok := dictDict(plist, "Size Info"); ok {
		info.SizeInfo = &SizeInfo{}
		if total, ok := dictUint64(sizeInfo, "Total Bytes"); ok {
			info.SizeInfo.TotalBytes = &total
		}
	}

	return info, nil
}

// DiskutilRun ports Diskutil.run(_:): executes diskutil with the given
// arguments and returns (stdout, stderr).
func DiskutilRun(arguments []string) ([]byte, []byte, error) {
	if _, err := exec.LookPath("diskutil"); err != nil {
		return nil, nil, weaveerrors.ErrGeneric("\"diskutil\" binary is not found in PATH")
	}

	cmd := exec.Command("diskutil", arguments...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	stdoutData := stdout.Bytes()
	stderrData := stderr.Bytes()

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, nil, weaveerrors.ErrGeneric("\"%s\" failed with exit code %d: %s",
				strings.Join(arguments, " "), exitErr.ExitCode(),
				FirstNonEmptyLine(string(stderrData), string(stdoutData)))
		}
		return nil, nil, weaveerrors.ErrGeneric("\"%s\" failed: %v", strings.Join(arguments, " "), err)
	}

	return stdoutData, stderrData, nil
}

// FirstNonEmptyLine ports Diskutil.FirstNonEmptyLine(_:).
func FirstNonEmptyLine(outputs ...string) string {
	for _, output := range outputs {
		for line := range strings.SplitSeq(output, "\n") {
			if line != "" {
				return line
			}
		}
	}
	return ""
}
