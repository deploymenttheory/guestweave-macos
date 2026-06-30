//go:build darwin

package clipguest

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/deploymenttheory/weave/internal/clipboard/wire"
)

// darwinBackend drives a macOS *guest* pasteboard for text and files.
//
// The agent runs over a non-interactive SSH channel, which is not part of the
// logged-in user's GUI ("Aqua") session, so it cannot reach the desktop
// pasteboard directly — and AppKit/NSPasteboard from a bare CLI (no app/run
// loop) crashes. Instead it delegates each operation to Apple's own session
// tools run inside the console user's session via `sudo -A launchctl asuser
// <uid> …`: pbpaste/pbcopy for text and osascript for the file channel. sudo
// reads its password from a SUDO_ASKPASS helper the host deployed, leaving
// piped clipboard data on stdin untouched.
//
// Text and a single file copy faithfully; image/rich representations and
// multi-file reads are not carried here (text + single file is the common case
// and the priority for macOS↔macOS).
type darwinBackend struct {
	stageDir string // staging dir for files received from the host
}

// darwinAskpass is where the host deploys the SUDO_ASKPASS helper carrying the
// clipboard user's password. Must match guestweaveagent/client.askpassRemotePath.
const darwinAskpass = "/tmp/weave-askpass"

func newBackend() (backend, error) {
	dir, err := os.MkdirTemp("", "weave-clip-guest-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	return &darwinBackend{stageDir: dir}, nil
}

// consoleUID returns the uid that owns /dev/console — the logged-in GUI user. It
// is 0 (root) at the login window, i.e. when no user is logged in and there is
// no pasteboard to share.
func consoleUID() (string, error) {
	out, err := exec.Command("/usr/bin/stat", "-f", "%u", "/dev/console").Output()
	if err != nil {
		return "", fmt.Errorf("read console uid: %w", err)
	}
	uid := strings.TrimSpace(string(out))
	if uid == "" || uid == "0" {
		return "", fmt.Errorf("no macOS GUI login session (console uid=%q) — clipboard needs a logged-in desktop", uid)
	}
	return uid, nil
}

// gui builds a command that runs name+args inside the console user's GUI session,
// elevated through sudo's askpass helper (which keeps stdin free for piped data).
func (b *darwinBackend) gui(name string, args ...string) (*exec.Cmd, error) {
	uid, err := consoleUID()
	if err != nil {
		return nil, err
	}
	full := append([]string{"-A", "/bin/launchctl", "asuser", uid, name}, args...)
	cmd := exec.Command("/usr/bin/sudo", full...)
	cmd.Env = append(os.Environ(), "SUDO_ASKPASS="+darwinAskpass)
	return cmd, nil
}

func (b *darwinBackend) pbpaste() ([]byte, error) {
	cmd, err := b.gui("/usr/bin/pbpaste")
	if err != nil {
		return nil, err
	}
	return cmd.Output()
}

func (b *darwinBackend) pbcopy(data []byte) error {
	cmd, err := b.gui("/usr/bin/pbcopy")
	if err != nil {
		return err
	}
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}

// osa runs an AppleScript snippet in the GUI session and returns its stdout.
func (b *darwinBackend) osa(script string) ([]byte, error) {
	cmd, err := b.gui("/usr/bin/osascript", "-e", script)
	if err != nil {
		return nil, err
	}
	return cmd.Output()
}

// clipboardFiles returns POSIX paths of files currently on the pasteboard, or
// nil when it holds no file (e.g. text only). Single-file fidelity.
func (b *darwinBackend) clipboardFiles() []string {
	out, err := b.osa(`POSIX path of (the clipboard as «class furl»)`)
	if err != nil {
		return nil // no file on the clipboard (coercion fails for text)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return nil
	}
	return []string{path}
}

// Stat hashes the pasteboard's text and file set so the engine detects changes;
// NSPasteboard's change counter is not available without AppKit.
func (b *darwinBackend) Stat() (uint64, error) {
	text, err := b.pbpaste()
	if err != nil {
		return 0, err
	}
	h := fnv.New64a()
	h.Write(text)
	for _, p := range b.clipboardFiles() {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return h.Sum64(), nil
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
	// A file copied in Finder also exposes its name as text; don't duplicate it.
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

// writeFiles stages each file and puts file references on the guest pasteboard
// via osascript so a Finder paste copies the real files.
func (b *darwinBackend) writeFiles(files []wire.DataFile) error {
	refs := make([]string, 0, len(files))
	for _, f := range files {
		path := filepath.Join(b.stageDir, f.Name)
		if err := os.WriteFile(path, f.Data, 0o600); err != nil {
			return fmt.Errorf("stage file %q: %w", f.Name, err)
		}
		refs = append(refs, fmt.Sprintf("POSIX file %s", appleScriptString(path)))
	}

	var script string
	if len(refs) == 1 {
		script = "set the clipboard to " + refs[0]
	} else {
		script = "set the clipboard to {" + strings.Join(refs, ", ") + "}"
	}
	if out, err := b.osa(script); err != nil {
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

// appleScriptString renders s as a quoted AppleScript string literal.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
