// Package macpb is the macOS NSPasteboard read/write shared by the host engine
// and the guest agent's darwin backend. It is threading-agnostic: callers that
// need main-thread affinity (the host engine) wrap these calls in
// mainthread.Do; the guest CLI agent calls them directly.
//go:build darwin

package macpb

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"

	appkit "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/appkit"
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"

	"github.com/deploymenttheory/weave/internal/clipboard/wire"
	"github.com/deploymenttheory/weave/internal/objcutil"
)

// FileURLType is the UTI for a single file reference on the pasteboard.
const FileURLType = "public.file-url"

var errWriteObjects = errors.New("NSPasteboard writeObjects failed")

// ChangeCount returns NSPasteboard's general change counter.
func ChangeCount() uint64 {
	return uint64(appkit.GeneralPasteboard().ChangeCount())
}

// Read captures the general pasteboard restricted to the allowed canonical
// formats. maxBytes drops any single item/file larger than the cap (0 =
// unlimited). The file channel is read only when wire.CanonFiles is allowed.
//
// A file copy is files-authoritative: when the pasteboard holds files, only the
// files are returned. A Finder file copy also advertises the file name as text
// and the icon as an image; syncing those alongside the file makes the host⇄guest
// round-trip lossy (the guest can only re-expose the file) and loop, so they are
// dropped when any file is present.
func Read(allowed map[wire.Canonical]bool, maxBytes int64) wire.Payload {
	pb := appkit.GeneralPasteboard()
	var payload wire.Payload

	seen := map[wire.Canonical]bool{}
	for _, nsType := range pb.Types() {
		uti := nsType.Description()
		canon, ok := wire.CanonicalForUTI(uti)
		if !ok || !allowed[canon] || seen[canon] || canon == wire.CanonFiles {
			continue
		}
		data := obj.Bytes(pb.DataForType(objcutil.NSStr(uti)))
		if len(data) == 0 || tooBig(int64(len(data)), maxBytes) {
			continue
		}
		payload.Items = append(payload.Items, wire.DataItem{Format: canon, Data: data})
		seen[canon] = true
	}

	if allowed[wire.CanonFiles] {
		for _, path := range fileURLs(pb) {
			data, err := os.ReadFile(path)
			if err != nil || tooBig(int64(len(data)), maxBytes) {
				continue
			}
			payload.Files = append(payload.Files, wire.DataFile{Name: filepath.Base(path), Data: data})
		}
	}

	// Files-authoritative: drop the incidental name-text/icon when files present.
	if len(payload.Files) > 0 {
		payload.Items = nil
	}

	return payload
}

// Write replaces the general pasteboard with the payload. Non-file
// representations are combined into a single pasteboard item; each file becomes
// its own item, staged under stageDir and referenced by a file URL.
func Write(p wire.Payload, stageDir string) error {
	pb := appkit.GeneralPasteboard()
	items := make([]obj.Object, 0, len(p.Files)+1)

	if len(p.Items) > 0 {
		item := appkit.NewPasteboardItem()
		for _, di := range p.Items {
			uti, ok := wire.UTIForCanonical(di.Format)
			if !ok {
				continue
			}
			item.SetDataForType(objcutil.BytesToNSData(di.Data), objcutil.NSStr(uti))
		}
		items = append(items, item)
	}

	for _, file := range p.Files {
		path := filepath.Join(stageDir, file.Name)
		if err := os.WriteFile(path, file.Data, 0o600); err != nil {
			return err
		}
		item := appkit.NewPasteboardItem()
		item.SetStringForType((&url.URL{Scheme: "file", Path: path}).String(), objcutil.NSStr(FileURLType))
		items = append(items, item)
	}

	pb.ClearContents()
	if len(items) == 0 {
		return nil
	}
	if !pb.WriteObjects(items) {
		return errWriteObjects
	}
	return nil
}

func tooBig(n, maxBytes int64) bool { return maxBytes > 0 && n > maxBytes }

// fileURLs returns the POSIX paths of every real file URL on the pasteboard,
// reading public.file-url canonically via readObjectsForClasses:[NSURL] so it
// works whether Finder stored the URL as data or a string (StringForType only
// reads the string form). Non-file URLs (http, etc.) are skipped via IsFileURL,
// matching the guest JXA reader.
func fileURLs(pb *appkit.Pasteboard) []string {
	res := pb.ReadObjectsForClassesOptions([]obj.Object{objcutil.NSClass("NSURL")}, nil)
	arr := foundation.ArrayFromID(obj.ID(res))
	if arr == nil {
		return nil
	}
	var paths []string
	for i := 0; i < arr.Count(); i++ {
		u, ok := obj.As(arr.ObjectAtIndex(i), "NSURL", foundation.URLFromID)
		if !ok || !u.IsFileURL() {
			continue
		}
		if p := u.Path(); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}
