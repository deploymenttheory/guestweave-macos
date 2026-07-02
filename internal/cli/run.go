//go:build darwin

package cli

import (
	weavecommand "github.com/deploymenttheory/guestweave/internal/command"
	vmrun "github.com/deploymenttheory/guestweave/internal/vm/run"
	"github.com/spf13/cobra"
)

func newRunCommand() *cobra.Command {
	opts := &vmrun.Options{}
	var (
		mounts    []string
		clipboard weavecommand.ClipboardFlagValues
	)

	cmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Run a VM",
		Long: `Run a VM, either headed (a native window with the guest display) or
headless (--no-graphics). The run process owns the VM for its lifetime:
stop, suspend, snapshot and clipboard commands talk to it from other
terminals.

This command owns the process's main thread and blocks until the VM stops.`,
		Example: `  weave run sequoia
  weave run sequoia --no-graphics
  weave run sequoia --vnc
  weave run ubuntu --mount ubuntu-24.04-live-server-arm64.iso
  weave run sequoia --dir ~/Projects --shared-dir builds:ro`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeLocalMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			// --mount <iso> is sugar for a read-only --disk attachment.
			for _, mount := range mounts {
				opts.Disk = append(opts.Disk, mount+":ro")
			}
			// --show-screen is a view-only mode: it needs the experimental VNC
			// server to capture from, and runs headless so no native window can
			// forward the operator's input into the guest.
			if opts.ShowScreen {
				opts.VNCExperimental = true
				opts.NoGraphics = true
			}
			opts.ClipboardOverride = clipboard.Override()
			opts.Reporter = runReporter{}
			opts.Name = args[0]
			if err := opts.Validate(); err != nil {
				return err
			}
			return runMainThread(cmd, func() error { return vmrun.Run(*opts) })
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&opts.NoGraphics, "no-graphics", false, "run headless, without a native window")
	flags.BoolVar(&opts.Graphics, "graphics", false, "force a native window even with VNC enabled")
	flags.BoolVar(&opts.Serial, "serial", false, "attach a serial console on a PTY")
	flags.StringVar(&opts.SerialPath, "serial-path", "", "attach the serial console to the given path instead of a PTY")
	flags.BoolVar(&opts.NoAudio, "no-audio", false, "disable audio devices")
	flags.BoolVar(&opts.Recovery, "recovery", false, "boot the macOS guest into recoveryOS")
	flags.BoolVar(&opts.Suspendable, "suspendable", false, "allow suspending this VM to disk (macOS 14+)")
	flags.BoolVar(&opts.Nested, "nested", false, "enable nested virtualization (macOS 15+, M3 or newer)")
	flags.StringVar(&opts.RosettaTag, "rosetta", "", "share the Rosetta runtime with a Linux guest under the given mount tag")

	flags.BoolVar(&opts.VNC, "vnc", false, "expose the VM over macOS Screen Sharing VNC")
	flags.BoolVar(&opts.VNCExperimental, "vnc-experimental", false, "expose the VM over the built-in experimental VNC server")
	flags.StringVar(&opts.VNCPassword, "vnc-password", "", "password for the experimental VNC server")
	flags.BoolVar(&opts.ShowScreen, "show-screen", false, "open a view-only browser viewer of the VM screen (implies headless)")

	flags.BoolVar(&opts.NoClipboard, "no-clipboard", false, "disable clipboard sharing")
	flags.BoolVar(&opts.Clipboard, "clipboard", false, "force-enable clipboard sharing")
	flags.StringVar(&opts.ClipboardUser, "clipboard-user", "weave", "guest user for clipboard agent installation")
	flags.StringVar(&opts.ClipboardPassword, "clipboard-password", "weave", "guest password for clipboard agent installation")
	flags.StringVar(&clipboard.Direction, "clipboard-direction", "", "clipboard direction override (disabled|bidirectional|hostToGuest|guestToHost)")
	flags.StringVar(&clipboard.Formats, "clipboard-formats", "", "clipboard formats override (csv of text,rich,image)")
	flags.StringVar(&clipboard.Files, "clipboard-files", "", "clipboard file-transfer override (on|off)")
	flags.StringVar(&clipboard.AllowedTypes, "clipboard-allowed-types", "", "allowed clipboard types override (csv, e.g. text/html,text/plain)")
	flags.StringVar(&clipboard.Audit, "clipboard-audit", "", "clipboard transfer auditing override (on|off)")
	flags.IntVar(&clipboard.SessionMbps, "clipboard-session-mbps", 0, "clipboard session bandwidth cap in Mbps")
	flags.IntVar(&clipboard.BandwidthPct, "clipboard-bandwidth-pct", 0, "clipboard bandwidth cap as a percent of link speed")
	flags.Int64Var(&clipboard.MaxBytes, "clipboard-max-bytes", 0, "maximum clipboard payload size in bytes")

	flags.StringArrayVar(&opts.Disk, "disk", nil, "attach an extra disk: path|device|nbd-URL[:ro][:opts] (repeatable)")
	flags.StringArrayVar(&mounts, "mount", nil, "attach an ISO read-only; sugar for --disk <iso>:ro (repeatable)")
	flags.StringArrayVar(&opts.USBStorage, "usb-storage", nil, "attach a disk image as USB mass storage (repeatable)")
	flags.StringVar(&opts.RootDiskOpts, "root-disk-opts", "", "root disk options (sync/caching modes)")
	flags.StringArrayVar(&opts.Dir, "dir", nil, "share a host directory: [name:]path[:ro] (repeatable)")
	flags.StringArrayVar(&opts.SharedDir, "shared-dir", nil, "share a host directory via virtiofs: path[:tag][:ro] (repeatable)")

	flags.StringVar(&opts.NetProfile, "net-profile", "", "network profile (nat|internet-only|isolated|vm-lab|bridged)")
	flags.StringArrayVar(&opts.NetBridged, "net-bridged", nil, "bridge a NIC onto the given host interface (repeatable)")
	flags.StringArrayVar(&opts.NetDevice, "net-device", nil, "add a NIC by spec (repeatable)")
	flags.BoolVar(&opts.NetSoftnet, "net-softnet", false, "use Softnet isolated networking")
	flags.StringVar(&opts.NetSoftnetAllow, "net-softnet-allow", "", "Softnet: allowed destination CIDRs (csv)")
	flags.StringVar(&opts.NetSoftnetBlock, "net-softnet-block", "", "Softnet: blocked destination CIDRs (csv)")
	flags.StringVar(&opts.NetSoftnetExpose, "net-softnet-expose", "", "Softnet: guest ports to expose (csv)")
	flags.BoolVar(&opts.NetHost, "net-host", false, "use host-only networking")

	flags.BoolVar(&opts.CaptureSystemKeys, "capture-system-keys", false, "forward system hotkeys into the guest window")
	flags.BoolVar(&opts.NoTrackpad, "no-trackpad", false, "use a plain pointer instead of the Mac trackpad device")
	flags.BoolVar(&opts.NoPointer, "no-pointer", false, "omit the pointing device")
	flags.BoolVar(&opts.NoKeyboard, "no-keyboard", false, "omit the keyboard device")
	return cmd
}
