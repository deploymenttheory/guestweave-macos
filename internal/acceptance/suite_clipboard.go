// Theme: the unified weave-guestd clipboard engine exercised end-to-end with a
// real text round-trip (clipboard), host ⇄ guest, for both a Linux and a macOS
// guest. This is the one flow that proves clipboard actually works, not just
// that the engine starts.
//
// The engine is the host side of `weave run` (default-on; here driven
// explicitly with --clipboard). It deploys the embedded weave-guestd agent over
// SSH and mirrors the host NSPasteboard with the guest clipboard. For the host
// side the suite drives pbcopy/pbpaste directly; for the guest side it reads and
// writes the guest clipboard over `weave ssh`.
//
// Linux guest (clipboard tools need a display): the suite clones the cirruslabs
// Ubuntu OCI image, installs xclip + Xvfb, and starts a headless X server on
// :99 so the agent's display discovery (and the verifying ssh commands) have a
// clipboard to talk to. Requires the image cached (weave pull) — skips cleanly
// otherwise.
//
// macOS guest (NSPasteboard, no extra tooling): set WEAVE_ACC_MACOS_GUEST to a
// provisioned, stopped macOS VM name (creds default weave/weave, override with
// WEAVE_ACC_MACOS_USER / WEAVE_ACC_MACOS_PASSWORD). Skips cleanly when unset.
//
// IMPORTANT: the embedded agent binaries must be present (make agent / make
// build); a plain `go build` embeds none and the engine disables itself. The
// Makefile `acceptance-clipboard` target builds them first.
//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/deploymenttheory/weave/internal/clipboard"
)

const (
	clipLinuxVM   = "acc-clip-linux"
	clipXvfbUnit  = "weave-xvfb"
	clipXvfbDisp  = ":99"
	clipBootWait  = 3 * time.Minute
	clipSyncWait  = 45 * time.Second
	clipStartWait = 60 * time.Second
	// clipTextToken is the placeholder in clipGuest.writeCmd replaced with the
	// shell-quoted text to copy. A sentinel (not a %-verb) avoids colliding with
	// the literal printf '%s' format in the command.
	clipTextToken = "__WEAVE_CLIP_TEXT__"
)

// clipGuest describes one guest under test: how to boot it, prepare it, and
// read/write its clipboard over `weave ssh`.
type clipGuest struct {
	os       string // "linux" | "darwin" (for labels/skip messages)
	vm       string
	user     string
	password string

	// prepare readies the guest clipboard channel (e.g. install xclip + start
	// Xvfb on Linux). nil when no preparation is needed (macOS). It returns a
	// skip reason when the guest cannot be prepared (e.g. no network for apt).
	prepare func(t *T, h *Harness, ssh sshRunner) (skip string)

	// readCmd prints the guest clipboard text to stdout.
	readCmd string
	// writeCmd writes %s (the text) into the guest clipboard.
	writeCmd string
}

func clipboardSuite() *Suite {
	cfg := loadNetBehaviorConfig()

	return &Suite{
		Name: "clipboard",
		Setup: func(h *Harness) error {
			// Reuse the real OCI cache so the multi-GB Linux image is not
			// re-downloaded into the isolated home.
			return shareOCICache(h)
		},
		Teardown: func(h *Harness) {
			h.Run("delete", clipLinuxVM)
		},
		Cases: []Case{
			{"linux: text round-trips host ⇄ guest", func(t *T, h *Harness) {
				if !imageAvailable(cfg.image) {
					t.Skip("no Linux image cached — run: weave pull %s (or set WEAVE_ACC_LINUX_IMAGE)", cfg.image)
				}
				ensureClone(t, h, cfg.image, clipLinuxVM)
				runClipboardRoundTrip(t, h, clipGuest{
					os:       "linux",
					vm:       clipLinuxVM,
					user:     cfg.user,
					password: cfg.password,
					prepare:  prepareLinuxClipboard,
					readCmd:  fmt.Sprintf("DISPLAY=%s xclip -selection clipboard -o 2>/dev/null", clipXvfbDisp),
					writeCmd: fmt.Sprintf("printf '%%s' %s | DISPLAY=%s xclip -selection clipboard -i", clipTextToken, clipXvfbDisp),
				})
			}},
			{"macos: text round-trips host ⇄ guest", func(t *T, h *Harness) {
				vm := os.Getenv("WEAVE_ACC_MACOS_GUEST")
				if vm == "" {
					t.Skip("set WEAVE_ACC_MACOS_GUEST to a provisioned, stopped macOS VM to exercise the macOS-guest path")
				}
				user := envOr("WEAVE_ACC_MACOS_USER", "weave")
				password := envOr("WEAVE_ACC_MACOS_PASSWORD", "weave")
				runClipboardRoundTrip(t, h, clipGuest{
					os:       "darwin",
					vm:       vm,
					user:     user,
					password: password,
					readCmd:  "pbpaste",
					writeCmd: "printf '%s' " + clipTextToken + " | pbcopy",
				})
			}},
		},
	}
}

// runClipboardRoundTrip boots the guest with the clipboard engine, prepares the
// guest clipboard channel, and asserts a text value copied on the host appears
// in the guest and vice versa.
func runClipboardRoundTrip(t *T, h *Harness, g clipGuest) {
	bg, err := h.Start(nil, "run", g.vm, "--no-graphics",
		"--clipboard", "--clipboard-formats", "text",
		"--clipboard-user", g.user, "--clipboard-password", g.password)
	if err != nil {
		t.Fatalf("starting run --clipboard for %q: %v", g.vm, err)
	}
	defer bg.Stop()

	if !waitRunning(h, g.vm, clipBootWait) {
		t.Fatalf("guest %q never reached running\n--- run log ---\n%s", g.vm, tail(bg.Output(), 30))
	}

	ssh := sshRunner{h: h, vm: g.vm, user: g.user, password: g.password}
	if !waitSSH(ssh) {
		t.Fatalf("guest %q booted but never became SSH-reachable\n%s", g.vm, channelDiagnostics(h, nil))
	}

	if g.prepare != nil {
		if skip := g.prepare(t, h, ssh); skip != "" {
			t.Skip("%s", skip)
		}
	}

	if !bg.waitForOutput(clipboard.StartedMarker, clipStartWait) {
		t.Fatalf("clipboard engine did not start:\n%s", tail(bg.Output(), 30))
	}

	// Host → guest: copy on the host, expect it in the guest clipboard.
	h2g := "weave-clip-h2g-" + g.os
	if err := hostPbcopy(h2g); err != nil {
		t.Fatalf("host pbcopy: %v", err)
	}
	if got, ok := pollGuestClipboard(ssh, g.readCmd, h2g, clipSyncWait); !ok {
		t.Errorf("host→guest text did not sync: guest clipboard=%q want %q\n--- run log ---\n%s",
			got, h2g, tail(bg.Output(), 30))
	} else {
		t.Evidence("host→guest: copied %q on host, read %q in %s guest", h2g, got, g.os)
	}

	// Guest → host: copy in the guest, expect it on the host pasteboard.
	g2h := "weave-clip-g2h-" + g.os
	writeCmd := strings.ReplaceAll(g.writeCmd, clipTextToken, shellSingleQuote(g2h))
	if out, code, err := ssh.RunGuest(context.Background(), writeCmd); err != nil || code != 0 {
		t.Fatalf("setting guest clipboard: code=%d err=%v out=%s", code, err, out)
	}
	if got, ok := pollHostClipboard(g2h, clipSyncWait); !ok {
		t.Errorf("guest→host text did not sync: host clipboard=%q want %q\n--- run log ---\n%s",
			got, g2h, tail(bg.Output(), 30))
	} else {
		t.Evidence("guest→host: copied %q in %s guest, read %q on host", g2h, g.os, got)
	}
}

// prepareLinuxClipboard installs the X11 clipboard tool and a headless X server
// the agent (and the verifying ssh commands) can talk to. Returns a skip reason
// when the packages cannot be installed (e.g. the guest has no network).
func prepareLinuxClipboard(t *T, h *Harness, ssh sshRunner) string {
	install := "sudo apt-get update -y && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y xclip xvfb"
	if out, code, _ := ssh.RunGuest(context.Background(), install); code != 0 {
		return "guest clipboard prep failed (xclip/xvfb install — needs guest network): " + strings.TrimSpace(tail(out, 5))
	}

	// Start a persistent headless X server on :99 (transient systemd unit so it
	// outlives the ssh session). -ac disables access control so any local user
	// (the agent and the verifying ssh) can use it without an Xauthority.
	start := fmt.Sprintf(
		"sudo systemctl reset-failed %[1]s 2>/dev/null; "+
			"sudo systemd-run --unit=%[1]s --description='weave clipboard test Xvfb' "+
			"/usr/bin/Xvfb %[2]s -ac -screen 0 1024x768x24",
		clipXvfbUnit, clipXvfbDisp)
	if out, code, _ := ssh.RunGuest(context.Background(), start); code != 0 {
		return "starting Xvfb failed: " + strings.TrimSpace(tail(out, 5))
	}

	// Wait for the X socket so the engine's first sync finds a display.
	probe := fmt.Sprintf("test -S /tmp/.X11-unix/X%s", strings.TrimPrefix(clipXvfbDisp, ":"))
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, code, _ := ssh.RunGuest(context.Background(), probe); code == 0 {
			t.Evidence("guest prepared: xclip + Xvfb on %s", clipXvfbDisp)
			return ""
		}
		time.Sleep(time.Second)
	}
	return "Xvfb did not create its socket on " + clipXvfbDisp
}

// waitSSH blocks until the guest answers `true` over ssh or a timeout elapses.
func waitSSH(ssh sshRunner) bool {
	deadline := time.Now().Add(clipBootWait)
	for time.Now().Before(deadline) {
		if _, code, err := ssh.RunGuest(context.Background(), "true"); err == nil && code == 0 {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// pollGuestClipboard reads the guest clipboard until it contains want or the
// timeout elapses, returning the last value read.
func pollGuestClipboard(ssh sshRunner, readCmd, want string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		out, _, _ := ssh.RunGuest(context.Background(), readCmd)
		last = strings.TrimSpace(out)
		if strings.Contains(last, want) {
			return last, true
		}
		if !time.Now().Before(deadline) {
			return last, false
		}
		time.Sleep(2 * time.Second)
	}
}

// pollHostClipboard reads the host pasteboard until it contains want or the
// timeout elapses, returning the last value read.
func pollHostClipboard(want string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		last, _ = hostPbpaste()
		last = strings.TrimSpace(last)
		if strings.Contains(last, want) {
			return last, true
		}
		if !time.Now().Before(deadline) {
			return last, false
		}
		time.Sleep(2 * time.Second)
	}
}

func hostPbcopy(s string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

func hostPbpaste() (string, error) {
	out, err := exec.Command("pbpaste").Output()
	return string(out), err
}

// shellSingleQuote wraps s in single quotes for safe interpolation into a remote
// shell command, escaping any embedded single quotes.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
