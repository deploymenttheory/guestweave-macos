// Resident-agent installation. The clipboard agent no longer runs as a transient
// SSH-stdio process: it must run *inside* the guest's GUI/login session so its
// pasteboard access is native. EnsureResident deploys weave-guestd and installs
// it as a macOS LaunchAgent (gui/<uid> domain) or a Linux systemd user service,
// then (re)starts it so it opens the virtio serial console and announces itself
// to the host. SSH is used only for this one-time-per-version setup; the live
// clipboard data path is the serial channel.
//go:build darwin

package client

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/deploymenttheory/weave/internal/guestweaveagent/agent"
	"github.com/deploymenttheory/weave/internal/guestweaveagent/agentbin"
	weavessh "github.com/deploymenttheory/weave/internal/ssh"
)

// residentLabel is the LaunchAgent / systemd unit identifier for the agent.
const residentLabel = "com.deploymenttheory.weave.guestd"

const installTimeout = 30 * time.Second

// EnsureResident installs (version-gated) and (re)starts the resident guest agent
// over SSH. It is safe to call every run: when the deployed version matches it
// skips the upload, and it always restarts the agent so it re-announces itself on
// the current run's serial channel.
func EnsureResident(ctx context.Context, ssh *weavessh.SSHClient, opts Options) error {
	binary, ok := agentbin.Binary(opts.GOOS, opts.GOARCH)
	if !ok {
		return fmt.Errorf("guestagent: no embedded agent for %s/%s", opts.GOOS, opts.GOARCH)
	}
	switch opts.GOOS {
	case "darwin":
		return ensureResidentDarwin(ctx, ssh, binary)
	case "linux":
		return ensureResidentLinux(ctx, ssh, binary)
	default:
		return fmt.Errorf("guestagent: resident agent unsupported on %s", opts.GOOS)
	}
}

// run executes a remote command and fails on a non-zero exit code.
func run(ctx context.Context, ssh *weavessh.SSHClient, command string) error {
	res, err := ssh.Execute(ctx, command, installTimeout)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("command failed (%d): %s: %s", res.ExitCode, command, strings.TrimSpace(res.Output))
	}
	return nil
}

// prepResult is the parsed output of the single prep round-trip: the guest home
// and the version of any already-deployed agent.
type prepResult struct {
	home    string
	version string
}

// prep does one SSH round-trip that makes the agent dirs and reports $HOME and
// the deployed version, keeping the connection churn low (high churn trips
// sshd's MaxStartups and shows up as flaky EOF/empty results).
func prep(ctx context.Context, ssh *weavessh.SSHClient, dirs, verPath string) (prepResult, error) {
	cmd := fmt.Sprintf("mkdir -p %s && printf 'HOME=%%s\\nVER=%%s\\n' \"$HOME\" \"$(cat %s 2>/dev/null)\"", dirs, verPath)
	res, err := ssh.Execute(ctx, cmd, installTimeout)
	if err != nil {
		return prepResult{}, err
	}
	if res.ExitCode != 0 {
		return prepResult{}, fmt.Errorf("prep failed (%d): %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	var out prepResult
	for line := range strings.SplitSeq(res.Output, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(line), "HOME="); ok {
			out.home = h
		} else if v, ok := strings.CutPrefix(strings.TrimSpace(line), "VER="); ok {
			out.version = v
		}
	}
	if out.home == "" {
		return prepResult{}, fmt.Errorf("empty guest home")
	}
	return out, nil
}

func ensureResidentDarwin(ctx context.Context, ssh *weavessh.SSHClient, binary []byte) error {
	// $HOME-relative paths the remote shell expands; absolute paths are derived
	// from the home returned by prep for the uploads.
	const (
		verRel   = "$HOME/.weave/weave-guestd.version"
		dirsRel  = "$HOME/.weave $HOME/Library/LaunchAgents"
		plistRel = "$HOME/Library/LaunchAgents/" + residentLabel + ".plist"
	)
	p, err := prep(ctx, ssh, dirsRel, verRel)
	if err != nil {
		return fmt.Errorf("guestagent: prep: %w", err)
	}
	var (
		binPath   = p.home + "/.weave/weave-guestd"
		verPath   = p.home + "/.weave/weave-guestd.version"
		logPath   = p.home + "/.weave/weave-guestd.log"
		plistPath = p.home + "/Library/LaunchAgents/" + residentLabel + ".plist"
	)
	restart := false
	if p.version != agent.Version {
		if err := ssh.Upload(ctx, bytes.NewReader(binary), binPath, 0o755); err != nil {
			return fmt.Errorf("guestagent: upload binary: %w", err)
		}
		if err := ssh.Upload(ctx, strings.NewReader(darwinPlist(residentLabel, binPath, logPath)), plistPath, 0o644); err != nil {
			return fmt.Errorf("guestagent: upload plist: %w", err)
		}
		if err := ssh.Upload(ctx, strings.NewReader(agent.Version), verPath, 0o644); err != nil {
			return fmt.Errorf("guestagent: write version: %w", err)
		}
		restart = true
	}
	// launchd already auto-loads ~/Library/LaunchAgents at GUI login, so usually
	// the agent is running by now. Don't bootout/bootstrap a live instance (that
	// races KeepAlive → exit 125): only bootstrap when it isn't loaded, and only
	// kickstart-restart when we just replaced the binary. Loading into our own
	// gui/$(id -u) domain needs no root.
	kick := ":"
	if restart {
		kick = `launchctl kickstart -k "gui/$uid/$l"`
	}
	script := fmt.Sprintf(
		`uid=$(id -u); l="%s"; pl="%s"; `+
			`if launchctl print "gui/$uid/$l" >/dev/null 2>&1; then %s; `+
			`else launchctl bootstrap "gui/$uid" "$pl"; fi`,
		residentLabel, plistRel, kick)
	if err := run(ctx, ssh, script); err != nil {
		return fmt.Errorf("guestagent: load LaunchAgent: %w", err)
	}
	return nil
}

func ensureResidentLinux(ctx context.Context, ssh *weavessh.SSHClient, binary []byte) error {
	const (
		verRel  = "$HOME/.weave/weave-guestd.version"
		dirsRel = "$HOME/.weave $HOME/.config/systemd/user"
	)
	p, err := prep(ctx, ssh, dirsRel, verRel)
	if err != nil {
		return fmt.Errorf("guestagent: prep: %w", err)
	}
	var (
		binPath  = p.home + "/.weave/weave-guestd"
		verPath  = p.home + "/.weave/weave-guestd.version"
		unitPath = p.home + "/.config/systemd/user/weave-guestd.service"
	)
	if p.version != agent.Version {
		if err := ssh.Upload(ctx, bytes.NewReader(binary), binPath, 0o755); err != nil {
			return fmt.Errorf("guestagent: upload binary: %w", err)
		}
		if err := ssh.Upload(ctx, strings.NewReader(linuxUnit(binPath)), unitPath, 0o644); err != nil {
			return fmt.Errorf("guestagent: upload unit: %w", err)
		}
		if err := ssh.Upload(ctx, strings.NewReader(agent.Version), verPath, 0o644); err != nil {
			return fmt.Errorf("guestagent: write version: %w", err)
		}
	}
	script := "systemctl --user daemon-reload; systemctl --user enable weave-guestd.service; systemctl --user restart weave-guestd.service"
	if err := run(ctx, ssh, script); err != nil {
		return fmt.Errorf("guestagent: start user service: %w", err)
	}
	return nil
}

func darwinPlist(label, binPath, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>serve-serial</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, label, binPath, logPath, logPath)
}

func linuxUnit(binPath string) string {
	return fmt.Sprintf(`[Unit]
Description=weave guest agent (clipboard)
After=graphical-session.target

[Service]
ExecStart=%s serve-serial
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`, binPath)
}
