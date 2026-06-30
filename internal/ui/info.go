// VM Info and About content: a full per-VM configuration summary (VM Info) and
// host/runtime-level facts (About). Both are presented in a selectable,
// copy-pasteable dialog. Run-time options that are not persisted in the VM
// config are plumbed in from the run command via SetRunInfo.
//go:build darwin

package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/deploymenttheory/weave/internal/ci"
	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/vmconfig"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
	"github.com/deploymenttheory/weave/internal/vmstorage"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
	appkit "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/appkit"
	corefoundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/corefoundation"
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
)

// processStart is set at package init (process start) and drives the About
// panel's uptime.
var processStart = time.Now()

// RunInfo carries the launch-time options that are applied to a VM but not
// persisted in its config.json, so VM Info can show the actual running shape.
// It is set by the run command before the window runs.
type RunInfo struct {
	Network     string // resolved network summary, e.g. "nat (default)" or "bridged: en0"
	Clipboard   string // resolved clipboard policy summary
	Disks       []string
	Dirs        []string
	SharedDirs  []string
	USBStorage  []string
	Rosetta     string
	Suspendable bool
	VNC         bool
	Nested      bool
	NoAudio     bool
	NoTrackpad  bool
	NoKeyboard  bool
	NoPointer   bool
	CaptureKeys bool
}

var runInfo RunInfo

// SetRunInfo records the applied launch-time options for the VM Info panel.
// Call before Run.
func SetRunInfo(r RunInfo) { runInfo = r }

// ── VM Info ──────────────────────────────────────────────────────────────────

// showVMInfo presents the VM's full applied configuration in a copy-pasteable
// dialog.
func showVMInfo() {
	showInfoSelectable("VM Info", vmInfoText())
}

func vmInfoText() string {
	c := activeVM.Config
	var b strings.Builder
	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%-16s %s\n", label+":", value)
		}
	}

	b.WriteString("— Identity —\n")
	line("Name", activeVM.Name)
	line("OS", string(c.OS))
	line("Architecture", string(c.Arch))
	line("Serial (ECID)", c.Serial())
	line("MAC address", macString(c))
	line("Config version", fmt.Sprintf("%d", c.Version))
	line("Directory", activeVMDir)

	b.WriteString("\n— Resources —\n")
	line("CPU", fmt.Sprintf("%d cores (min %d)", c.CPUCount, c.CPUCountMin))
	line("Memory", fmt.Sprintf("%s (min %s)", formatMiB(c.MemorySize), formatMiB(c.MemorySizeMin)))
	line("Disk", diskSummary(c))

	b.WriteString("\n— Display & devices —\n")
	line("Display", displaySummary(c))
	line("Input/audio", deviceSummary())

	b.WriteString("\n— Networking —\n")
	line("Network", emptyDefault(runInfo.Network, "nat (default)"))
	line("NIC topology", nicSummary(c))

	b.WriteString("\n— Sharing & clipboard —\n")
	line("Clipboard", runInfo.Clipboard)
	line("Mounted disks", strings.Join(runInfo.Disks, ", "))
	line("Shared dirs", strings.Join(append(append([]string{}, runInfo.Dirs...), runInfo.SharedDirs...), ", "))
	line("USB storage", strings.Join(runInfo.USBStorage, ", "))
	line("Rosetta", runInfo.Rosetta)

	if opts := runtimeOptions(); opts != "" {
		b.WriteString("\n— Run options —\n")
		line("Enabled", opts)
	}

	return strings.TrimRight(b.String(), "\n")
}

func macString(c *vmconfig.VMConfig) string {
	if c.MACAddress == nil {
		return ""
	}
	return c.MACAddress.String()
}

func displaySummary(c *vmconfig.VMConfig) string {
	s := c.Display.String()
	if c.DisplayRefit != nil && *c.DisplayRefit {
		s += " (auto-refit)"
	}
	return s
}

func deviceSummary() string {
	var on []string
	add := func(name string, disabled bool) {
		if !disabled {
			on = append(on, name)
		}
	}
	add("trackpad", runInfo.NoTrackpad)
	add("keyboard", runInfo.NoKeyboard)
	add("pointer", runInfo.NoPointer)
	add("audio", runInfo.NoAudio)
	if len(on) == 0 {
		return "none"
	}
	return strings.Join(on, ", ")
}

func diskSummary(c *vmconfig.VMConfig) string {
	format := string(c.DiskFormat)
	if format == "" {
		format = "raw"
	}
	if activeVMDir != "" {
		if gb, err := vmdirectory.NewVMDirectory(activeVMDir).DiskSizeGB(); err == nil && gb > 0 {
			return fmt.Sprintf("%d GB (%s)", gb, format)
		}
	}
	return format
}

func nicSummary(c *vmconfig.VMConfig) string {
	if len(c.NICs) == 0 {
		return "single NAT NIC (legacy)"
	}
	parts := make([]string, 0, len(c.NICs))
	for i, nic := range c.NICs {
		parts = append(parts, fmt.Sprintf("nic%d %s", i, nicDesc(nic)))
	}
	return strings.Join(parts, "; ")
}

func nicDesc(nic vmconfig.NICConfig) string {
	desc := string(nic.Mode)
	switch {
	case nic.BridgedInterface != "":
		desc += " (" + nic.BridgedInterface + ")"
	case nic.VmnetMode != "":
		desc += " (" + nic.VmnetMode + ")"
	}
	if nic.IsPrimary {
		desc += " [primary]"
	}
	if nic.MACAddress != "" {
		desc += " " + nic.MACAddress
	}
	return desc
}

func runtimeOptions() string {
	var on []string
	if runInfo.Suspendable {
		on = append(on, "suspendable")
	}
	if runInfo.VNC {
		on = append(on, "vnc")
	}
	if runInfo.Nested {
		on = append(on, "nested-virtualization")
	}
	if runInfo.CaptureKeys {
		on = append(on, "capture-system-keys")
	}
	return strings.Join(on, ", ")
}

// ── About / global ───────────────────────────────────────────────────────────

func aboutText() string {
	var b strings.Builder
	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%-14s %s\n", label+":", value)
		}
	}

	b.WriteString("— weave —\n")
	line("Version", weaveVersion())
	line("Uptime", time.Since(processStart).Round(time.Second).String())

	b.WriteString("\n— VMs —\n")
	macRun, macTotal, linRun, linTotal := vmSlotStats()
	line("macOS", fmt.Sprintf("%d running / %d total (host limit 2)", macRun, macTotal))
	line("Linux", fmt.Sprintf("%d running / %d total (unlimited)", linRun, linTotal))

	b.WriteString("\n— Host —\n")
	pi := foundation.NewProcessInfo()
	line("macOS", pi.OperatingSystemVersionString())
	line("Architecture", string(weaveplatform.CurrentArchitecture()))
	line("Host memory", formatByteSize(int64(pi.PhysicalMemory())))

	b.WriteString("\n— Storage —\n")
	if cfg, err := weaveconfig.NewConfig(); err == nil {
		line("Home", cfg.WeaveHomeDir)
	}
	line("Repository", "https://github.com/deploymenttheory/guestweave-macos")

	return strings.TrimRight(b.String(), "\n")
}

func weaveVersion() string {
	if v := ci.CIVersion(); v != "" {
		return v
	}
	return "dev (unversioned build)"
}

// vmSlotStats counts running/total VMs by guest OS for the About panel. macOS
// guests are capped at 2 concurrently by the Virtualization framework; Linux
// guests are unlimited.
func vmSlotStats() (macRunning, macTotal, linuxRunning, linuxTotal int) {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return
	}
	entries, err := storage.List()
	if err != nil {
		return
	}
	for _, e := range entries {
		cfg, err := vmconfig.NewVMConfigFromURL(e.VMDir.ConfigURL())
		if err != nil {
			continue
		}
		running, _ := e.VMDir.Running()
		switch cfg.OS {
		case weaveplatform.OSDarwin:
			macTotal++
			if running {
				macRunning++
			}
		case weaveplatform.OSLinux:
			linuxTotal++
			if running {
				linuxRunning++
			}
		}
	}
	return
}

// ── helpers ──────────────────────────────────────────────────────────────────

func formatMiB(bytes uint64) string { return formatByteSize(int64(bytes)) }

func emptyDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// formatByteSize renders a byte count in compact IEC units (e.g. "8 GiB").
func formatByteSize(n int64) string {
	const unit = 1024
	const suffixes = "KMGTPE"
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < len(suffixes)-1; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %ciB", float64(n)/float64(div), suffixes[exp])
}

// ── selectable dialog ────────────────────────────────────────────────────────

// showInfoSelectable shows a modal info dialog whose body text is selectable and
// copy-pasteable (a read-only, monospaced NSTextField accessory), unlike the
// plain runAlert whose informative text cannot be selected.
func showInfoSelectable(title, body string) {
	const width = 520.0

	field := objcutil.AllocClass("NSTextField").Send(purego.RegisterName("init"))
	// A monospaced font keeps the column-aligned labels lined up.
	font := purego.ID(purego.GetClass("NSFont")).Send(purego.RegisterName("userFixedPitchFontOfSize:"), float64(11))

	tf := appkit.TextFieldFromID(field)
	tf.WithStringValue(body)
	tf.WithSelectable(true)
	tf.WithEditable(false)
	tf.WithBordered(false)
	tf.WithDrawsBackground(false)
	tf.WithFont(appkit.FontFromID(font))
	tf.WithPreferredMaxLayoutWidth(width)
	tf.WithFrame(corefoundation.CGRect{Size: corefoundation.CGSize{Width: width, Height: 16}})
	field.Send(purego.RegisterName("sizeToFit"))

	alert := appkit.NewAlert()
	alert.WithMessageText(title)
	alert.WithAccessoryView(tf)
	alert.RunModal()
}
