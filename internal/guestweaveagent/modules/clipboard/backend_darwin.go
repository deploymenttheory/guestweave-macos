//go:build darwin

package clipguest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/deploymenttheory/weave/internal/clipboard/wire"
)

// darwinBackend drives a macOS *guest* pasteboard for text and files.
//
// weave-guestd now runs **resident inside the logged-in user's GUI ("Aqua")
// session** (a LaunchAgent in the gui/<uid> domain), so it can reach the desktop
// pasteboard with the plain command-line tools directly — no `sudo`, no
// `launchctl asuser`, no SUDO_ASKPASS. (AppKit/NSPasteboard from a bare Go CLI
// still crashes, so we shell out to Apple's tools rather than link AppKit.)
//
//   - text: pbpaste / pbcopy.
//   - files: a small JXA (osascript -l JavaScript) helper that talks to
//     NSPasteboard properly — reading only *real* file URLs (isFileURL, no text
//     coercion) and writing public.file-url via writeObjects so a Finder paste
//     copies the real files. osascript hosts a full app context, so AppKit works
//     there even though our bare-CLI agent cannot use it.
//   - change detection: NSPasteboard's real changeCount via the same JXA helper.
//
// A file copy is treated as files-only (see Read): macOS Finder also puts the
// file name as text and the icon as an image on the pasteboard, but syncing
// those alongside the file makes the host⇄guest round-trip lossy and loop, so we
// keep just the files when any are present.
type darwinBackend struct {
	stageDir string // staging dir for files received from the host
	jsPath   string // deployed JXA pasteboard helper
}

// pbScript is a JXA NSPasteboard helper, run via `osascript -l JavaScript`.
// osascript provides the app context AppKit needs (which a bare Go CLI lacks).
//
//	count                  -> prints NSPasteboard.changeCount
//	readfiles              -> prints one POSIX path per real file URL (isFileURL)
//	writefiles <p1> <p2>…  -> clears and writes those paths as public.file-url
const pbScript = `ObjC.import('AppKit');
function run(argv) {
  var pb = $.NSPasteboard.generalPasteboard;
  var cmd = argv[0];
  if (cmd === 'count') { return String(pb.changeCount); }
  if (cmd === 'readfiles') {
    var arr = pb.readObjectsForClassesOptions($([$.NSURL]), $());
    var out = [];
    if (arr && !arr.isNil()) {
      for (var i = 0; i < arr.count; i++) {
        var u = arr.objectAtIndex(i);
        if (u.isFileURL) out.push(ObjC.unwrap(u.path));
      }
    }
    return out.join('\n');
  }
  if (cmd === 'writefiles') {
    pb.clearContents;
    var urls = [];
    for (var i = 1; i < argv.length; i++) { urls.push($.NSURL.fileURLWithPath(argv[i])); }
    pb.writeObjects($(urls));
    return 'ok';
  }
  return 'unknown';
}`

func newBackend() (backend, error) {
	dir, err := os.MkdirTemp("", "weave-clip-guest-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	jsPath := filepath.Join(dir, "pb.js")
	if err := os.WriteFile(jsPath, []byte(pbScript), 0o644); err != nil {
		return nil, fmt.Errorf("write pasteboard helper: %w", err)
	}
	return &darwinBackend{stageDir: dir, jsPath: jsPath}, nil
}

// jxa runs the JXA helper with cmd (+ args) and returns its stdout. The agent is
// in-session, so osascript reaches the desktop pasteboard directly.
func (b *darwinBackend) jxa(cmd string, args ...string) ([]byte, error) {
	c := exec.Command("/usr/bin/osascript", append([]string{"-l", "JavaScript", b.jsPath, cmd}, args...)...)
	return c.Output()
}

func (b *darwinBackend) pbpaste() ([]byte, error) {
	return exec.Command("/usr/bin/pbpaste").Output()
}

func (b *darwinBackend) pbcopy(data []byte) error {
	cmd := exec.Command("/usr/bin/pbcopy")
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}

// clipboardFiles returns the POSIX paths of *real* file URLs on the pasteboard
// (never text coerced into a path).
func (b *darwinBackend) clipboardFiles() []string {
	out, err := b.jxa("readfiles")
	if err != nil {
		return nil
	}
	var paths []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// Stat returns NSPasteboard's real change counter so the engine detects changes.
func (b *darwinBackend) Stat() (uint64, error) {
	out, err := b.jxa("count")
	if err != nil {
		return 0, fmt.Errorf("pasteboard changeCount: %w", err)
	}
	return strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
}

func (b *darwinBackend) Read(allowed map[wire.Canonical]bool) (wire.Payload, error) {
	var p wire.Payload

	if allowed[wire.CanonFiles] {
		for _, path := range b.clipboardFiles() {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			p.Files = append(p.Files, wire.DataFile{Name: filepath.Base(path), Data: data})
		}
	}
	// Files-only when files are present: a Finder file copy also carries the name
	// as text, which we must not also sync (it would make the round-trip lossy).
	if allowed[wire.CanonPlainText] && len(p.Files) == 0 {
		if text, err := b.pbpaste(); err == nil && len(text) > 0 {
			p.Items = append(p.Items, wire.DataItem{Format: wire.CanonPlainText, Data: text})
		}
	}
	return p, nil
}

func (b *darwinBackend) Write(p wire.Payload) error {
	if len(p.Files) > 0 {
		return b.writeFiles(p.Files)
	}
	if data, ok := plainText(p.Items); ok {
		return b.pbcopy(data)
	}
	return nil
}

// writeFiles stages each file and puts real file URLs on the guest pasteboard
// (public.file-url via JXA writeObjects) so a Finder paste copies the files.
func (b *darwinBackend) writeFiles(files []wire.DataFile) error {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		path := filepath.Join(b.stageDir, f.Name)
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			return fmt.Errorf("stage file %q: %w", f.Name, err)
		}
		paths = append(paths, path)
	}
	if out, err := b.jxa("writefiles", paths...); err != nil {
		return fmt.Errorf("set clipboard files: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// plainText returns the plain-text representation to write, preferring an
// explicit text/plain item and otherwise the first item's bytes.
func plainText(items []wire.DataItem) ([]byte, bool) {
	for _, it := range items {
		if it.Format == wire.CanonPlainText {
			return it.Data, true
		}
	}
	if len(items) > 0 {
		return items[0].Data, true
	}
	return nil, false
}
