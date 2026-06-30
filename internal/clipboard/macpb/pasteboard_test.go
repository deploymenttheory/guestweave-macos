//go:build darwin

package macpb

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	appkit "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/appkit"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"

	"github.com/deploymenttheory/weave/internal/clipboard/wire"
	"github.com/deploymenttheory/weave/internal/objcutil"
)

// TestRoundTrip writes multiple representations to the real host pasteboard and
// reads them back, asserting full format fidelity. It clobbers the clipboard,
// so it runs only when WEAVE_PASTEBOARD_TEST=1. NSPasteboard is called directly
// (no main-thread dispatch): a test binary has no main run loop, and the simple
// read/write operations are safe off the main thread.
func TestRoundTrip(t *testing.T) {
	if os.Getenv("WEAVE_PASTEBOARD_TEST") == "" {
		t.Skip("set WEAVE_PASTEBOARD_TEST=1 to run (clobbers the host clipboard)")
	}

	allowed := map[wire.Canonical]bool{
		wire.CanonPlainText: true,
		wire.CanonRTF:       true,
		wire.CanonHTML:      true,
	}
	want := wire.Payload{Items: []wire.DataItem{
		{Format: wire.CanonRTF, Data: []byte(`{\rtf1\ansi\ansicpg1252 hello}`)},
		{Format: wire.CanonHTML, Data: []byte("<b>hello</b>")},
		{Format: wire.CanonPlainText, Data: []byte("hello")},
	}}

	if err := Write(want, t.TempDir()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := Read(allowed, 0)
	byFormat := map[wire.Canonical][]byte{}
	for _, item := range got.Items {
		byFormat[item.Format] = item.Data
	}
	for _, item := range want.Items {
		data, ok := byFormat[item.Format]
		if !ok {
			t.Errorf("format %q missing after round-trip", item.Format)
			continue
		}
		if !bytes.Equal(data, item.Data) {
			t.Errorf("format %q: got %q, want %q", item.Format, data, item.Data)
		}
	}
}

// TestFileRoundTrip stages a file on the pasteboard and reads it back.
func TestFileRoundTrip(t *testing.T) {
	if os.Getenv("WEAVE_PASTEBOARD_TEST") == "" {
		t.Skip("set WEAVE_PASTEBOARD_TEST=1 to run (clobbers the host clipboard)")
	}

	want := wire.Payload{Files: []wire.DataFile{
		{Name: "hello.txt", Data: []byte("file contents")},
		{Name: "second.bin", Data: bytes.Repeat([]byte{0x07}, 2048)},
	}}
	if err := Write(want, t.TempDir()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := Read(map[wire.Canonical]bool{wire.CanonFiles: true}, 0)
	if len(got.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(got.Files))
	}
	if got.Files[0].Name != "hello.txt" || !bytes.Equal(got.Files[0].Data, []byte("file contents")) {
		t.Errorf("file 0 mismatch: %+v", got.Files[0])
	}
	if len(got.Files[1].Data) != 2048 {
		t.Errorf("file 1 size = %d, want 2048", len(got.Files[1].Data))
	}
}

// TestFinderDataURLRead reproduces a real Finder file copy: Finder stores
// public.file-url as NSData (the URL string bytes), not a string, which the old
// StringForType reader returned empty for. Read must capture it as a file.
func TestFinderDataURLRead(t *testing.T) {
	if os.Getenv("WEAVE_PASTEBOARD_TEST") == "" {
		t.Skip("set WEAVE_PASTEBOARD_TEST=1 to run (clobbers the host clipboard)")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "finder.txt")
	want := []byte("finder copied me")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	// Set public.file-url as DATA (the Finder form), not via SetStringForType.
	pb := appkit.GeneralPasteboard()
	pb.ClearContents()
	item := appkit.NewPasteboardItem()
	urlBytes := []byte((&url.URL{Scheme: "file", Path: path}).String())
	item.SetDataForType(objcutil.BytesToNSData(urlBytes), objcutil.NSStr(FileURLType))
	if !pb.WriteObjects([]obj.Object{item}) {
		t.Fatal("WriteObjects failed")
	}

	got := Read(map[wire.Canonical]bool{wire.CanonFiles: true}, 0)
	if len(got.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(got.Files))
	}
	if got.Files[0].Name != "finder.txt" || !bytes.Equal(got.Files[0].Data, want) {
		t.Errorf("file mismatch: %+v", got.Files[0])
	}
}
