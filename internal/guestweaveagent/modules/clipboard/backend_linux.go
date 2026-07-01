//go:build linux

package clipguest

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/deploymenttheory/guestweave/internal/clipboard/wire"
)

// linuxBackend drives the guest clipboard through the standard CLI tools:
// wl-clipboard (wl-copy/wl-paste) under Wayland, falling back to xclip under
// X11. These tools expose only a single representation per copy (each
// invocation replaces the selection), so Write sets the single richest
// representation available (files > image > html > rtf > plain). Read can pull
// every advertised target the policy allows.
type linuxBackend struct {
	stageDir    string
	listTargets func() ([]string, error)
	paste       func(target string) ([]byte, error)
	copy        func(target string, data []byte) error
}

func newBackend() (backend, error) {
	dir, err := os.MkdirTemp("", "weave-clipboard-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}

	// The agent is launched over a non-login SSH channel, so DISPLAY /
	// WAYLAND_DISPLAY (and XAUTHORITY / XDG_RUNTIME_DIR) are usually unset even
	// when a graphical session is running. Discover the active session — or a
	// headless Xvfb — and export what the CLI clipboard tools need. Setting them
	// on this process makes every exec.Command below inherit them.
	setupDisplayEnv()

	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	x11 := os.Getenv("DISPLAY") != ""

	if wayland && haveAll("wl-paste", "wl-copy") {
		return &linuxBackend{stageDir: dir, listTargets: wlListTargets, paste: wlPaste, copy: wlCopy}, nil
	}
	if x11 && haveAll("xclip") {
		return &linuxBackend{stageDir: dir, listTargets: xclipListTargets, paste: xclipPaste, copy: xclipCopy}, nil
	}

	switch {
	case !wayland && !x11:
		return nil, fmt.Errorf("no display server found (need DISPLAY or WAYLAND_DISPLAY; clipboard needs a graphical session or a headless Xvfb)")
	case wayland:
		return nil, fmt.Errorf("Wayland display %q found but wl-clipboard (wl-copy/wl-paste) is not installed", os.Getenv("WAYLAND_DISPLAY"))
	default:
		return nil, fmt.Errorf("X11 display %q found but xclip is not installed", os.Getenv("DISPLAY"))
	}
}

// setupDisplayEnv discovers the guest's graphical session and exports the
// environment the clipboard CLIs need (DISPLAY/WAYLAND_DISPLAY, XDG_RUNTIME_DIR,
// XAUTHORITY) when they are not already set. It prefers Wayland (a wayland-*
// socket in the runtime dir), then falls back to X11 (an X socket under
// /tmp/.X11-unix — covers Xorg, Xwayland and a headless Xvfb). An environment
// supplied by the caller (e.g. a systemd user service) is left untouched.
func setupDisplayEnv() {
	uid := os.Getuid()

	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", uid)
		if fi, err := os.Stat(runtimeDir); err == nil && fi.IsDir() {
			_ = os.Setenv("XDG_RUNTIME_DIR", runtimeDir)
		}
	}

	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != "" {
		return // honour an explicitly provided session
	}

	if sock := firstWaylandSocket(runtimeDir); sock != "" {
		_ = os.Setenv("WAYLAND_DISPLAY", sock)
		return
	}

	if disp := firstX11Display(); disp != "" {
		_ = os.Setenv("DISPLAY", disp)
		if os.Getenv("XAUTHORITY") == "" {
			if xauth := discoverXAuthority(uid); xauth != "" {
				_ = os.Setenv("XAUTHORITY", xauth)
			}
		}
	}
}

// firstWaylandSocket returns the name (e.g. "wayland-0") of the first Wayland
// display socket in runtimeDir, ignoring the ".lock" companions.
func firstWaylandSocket(runtimeDir string) string {
	if runtimeDir == "" {
		return ""
	}
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "wayland-") && !strings.HasSuffix(name, ".lock") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// firstX11Display returns the lowest-numbered X display (e.g. ":0") that has a
// socket under /tmp/.X11-unix.
func firstX11Display() string {
	entries, err := os.ReadDir("/tmp/.X11-unix")
	if err != nil {
		return ""
	}
	nums := make([]int, 0, len(entries))
	for _, e := range entries {
		name := e.Name() // "X0", "X99", …
		if !strings.HasPrefix(name, "X") {
			continue
		}
		if n, err := strconv.Atoi(name[1:]); err == nil {
			nums = append(nums, n)
		}
	}
	if len(nums) == 0 {
		return ""
	}
	slices.Sort(nums)
	return fmt.Sprintf(":%d", nums[0])
}

// discoverXAuthority returns the first plausible X authority file for the
// running user, or "" when none is found (a headless Xvfb started with -ac
// needs no authority).
func discoverXAuthority(uid int) string {
	candidates := []string{
		os.Getenv("HOME") + "/.Xauthority",
		fmt.Sprintf("/run/user/%d/gdm/Xauthority", uid),
		fmt.Sprintf("/run/user/%d/.mutter-Xwaylandauth", uid),
	}
	for _, path := range candidates {
		if path == "/.Xauthority" {
			continue
		}
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			return path
		}
	}
	return ""
}

func (b *linuxBackend) Stat() (uint64, error) {
	h := fnv.New64a()
	targets, _ := b.listTargets()
	for _, t := range targets {
		h.Write([]byte(t))
		h.Write([]byte{0})
	}
	for _, target := range []string{"text/plain;charset=utf-8", "text/uri-list"} {
		if data, err := b.paste(target); err == nil {
			h.Write(data)
		}
	}
	return h.Sum64(), nil
}

func (b *linuxBackend) Read(allowed map[wire.Canonical]bool) (wire.Payload, error) {
	var payload wire.Payload

	targets, err := b.listTargets()
	if err != nil {
		return payload, err
	}

	seen := map[wire.Canonical]bool{}
	for _, canon := range wire.AllCanonical() {
		if !allowed[canon] || canon == wire.CanonFiles || seen[canon] {
			continue
		}
		mime, ok := wire.LinuxMIMEForCanonical(canon)
		if !ok || !hasTarget(targets, mime) {
			continue
		}
		data, err := b.paste(mime)
		if err != nil || len(data) == 0 {
			continue
		}
		payload.Items = append(payload.Items, wire.DataItem{Format: canon, Data: data})
		seen[canon] = true
	}

	if allowed[wire.CanonFiles] && hasTarget(targets, "text/uri-list") {
		if data, err := b.paste("text/uri-list"); err == nil {
			for _, path := range parseURIList(data) {
				contents, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				payload.Files = append(payload.Files, wire.DataFile{Name: filepath.Base(path), Data: contents})
			}
		}
	}

	// Files-authoritative: a file copy also advertises the path/name as text;
	// keep only the files so the host⇄guest round-trip converges (matches the
	// host-side macpb.Read behaviour).
	if len(payload.Files) > 0 {
		payload.Items = nil
	}

	return payload, nil
}

func (b *linuxBackend) Write(p wire.Payload) error {
	if len(p.Files) > 0 {
		var list bytes.Buffer
		for _, file := range p.Files {
			path := filepath.Join(b.stageDir, file.Name)
			if err := os.WriteFile(path, file.Data, 0o600); err != nil {
				return fmt.Errorf("stage file %q: %w", file.Name, err)
			}
			list.WriteString((&url.URL{Scheme: "file", Path: path}).String())
			list.WriteString("\r\n")
		}
		return b.copy("text/uri-list", list.Bytes())
	}

	if best, ok := richestItem(p.Items); ok {
		mime, _ := wire.LinuxMIMEForCanonical(best.Format)
		return b.copy(mime, best.Data)
	}
	return nil
}

// richestItem picks the highest-fidelity item: image first, then html, rtf,
// plain. Returns false when there are no items.
func richestItem(items []wire.DataItem) (wire.DataItem, bool) {
	priority := map[wire.Canonical]int{
		wire.CanonPNG: 5, wire.CanonTIFF: 5, wire.CanonPDF: 5,
		wire.CanonHTML: 4, wire.CanonRTF: 3, wire.CanonPlainText: 1,
	}
	best := wire.DataItem{}
	bestScore := -1
	for _, item := range items {
		if priority[item.Format] > bestScore {
			best, bestScore = item, priority[item.Format]
		}
	}
	return best, bestScore >= 0
}

// hasTarget reports whether the clipboard advertises a MIME target matching
// mime, comparing the part before any ";charset=" parameter and accepting the
// X11 plain-text aliases.
func hasTarget(targets []string, mime string) bool {
	want := baseMIME(mime)
	for _, t := range targets {
		if baseMIME(t) == want {
			return true
		}
		if want == "text/plain" && (t == "UTF8_STRING" || t == "STRING" || t == "TEXT") {
			return true
		}
	}
	return false
}

func baseMIME(m string) string {
	base, _, _ := strings.Cut(m, ";")
	return strings.TrimSpace(base)
}

func parseURIList(data []byte) []string {
	var paths []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if parsed, err := url.Parse(line); err == nil && parsed.Scheme == "file" {
			paths = append(paths, parsed.Path)
		}
	}
	return paths
}

func haveAll(tools ...string) bool {
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return false
		}
	}
	return true
}

// ── Wayland (wl-clipboard) ───────────────────────────────────────────────────

func wlListTargets() ([]string, error) {
	out, err := exec.Command("wl-paste", "--list-types").Output()
	if err != nil {
		return nil, nil // empty clipboard exits non-zero; treat as no targets
	}
	return splitLines(out), nil
}

func wlPaste(target string) ([]byte, error) {
	return exec.Command("wl-paste", "--no-newline", "--type", target).Output()
}

func wlCopy(target string, data []byte) error {
	cmd := exec.Command("wl-copy", "--type", target)
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}

// ── X11 (xclip) ──────────────────────────────────────────────────────────────

func xclipListTargets() ([]string, error) {
	out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
	if err != nil {
		return nil, nil
	}
	return splitLines(out), nil
}

func xclipPaste(target string) ([]byte, error) {
	return exec.Command("xclip", "-selection", "clipboard", "-t", target, "-o").Output()
}

func xclipCopy(target string, data []byte) error {
	cmd := exec.Command("xclip", "-selection", "clipboard", "-t", target, "-i")
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}

func splitLines(b []byte) []string {
	var lines []string
	for line := range strings.SplitSeq(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
