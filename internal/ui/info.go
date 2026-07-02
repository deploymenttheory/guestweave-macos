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

	"github.com/deploymenttheory/guestweave/branding"
	"github.com/deploymenttheory/guestweave/internal/ci"
	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
	"github.com/deploymenttheory/guestweave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/guestweave/internal/platform"
	vmconfig "github.com/deploymenttheory/guestweave/internal/vm/config"
	"github.com/deploymenttheory/guestweave/internal/vm/layout"
	vmstorage "github.com/deploymenttheory/guestweave/internal/vm/storage"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
	appkit "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/appkit"
	corefoundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/corefoundation"
	coretext "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/coretext"
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
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
		if gb, err := layout.NewVMDirectory(activeVMDir).DiskSizeGB(); err == nil && gb > 0 {
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

const repoURL = "https://github.com/deploymenttheory/guestweave-macos"

// aboutInfoDocument builds the two-column info grid for the About window: bold
// section headers, then monospaced rows with secondary-colored labels and
// primary-colored values. The wordmark, version, and link are separate views.
func aboutInfoDocument() *foundation.MutableAttributedString {
	doc := foundation.NewMutableAttributedString()
	header := fontAttrs(aboutFont("boldSystemFontOfSize:", 12))
	label := textAttrs(aboutFont("userFixedPitchFontOfSize:", 11), secondaryColor())
	value := fontAttrs(aboutFont("userFixedPitchFontOfSize:", 11))

	first := true
	section := func(title string) {
		if !first {
			doc.AppendAttributedString(attrRun("\n", value))
		}
		first = false
		doc.AppendAttributedString(attrRun(title+"\n", header))
	}
	row := func(name, val string) {
		if val != "" {
			doc.AppendAttributedString(attrRun(fmt.Sprintf("  %-13s", name), label))
			doc.AppendAttributedString(attrRun(val+"\n", value))
		}
	}

	section("Runtime")
	row("Uptime", time.Since(processStart).Round(time.Second).String())

	section("Virtual machines")
	macRun, macTotal, linRun, linTotal := vmSlotStats()
	row("macOS", fmt.Sprintf("%d of %d running   (host limit 2)", macRun, macTotal))
	row("Linux", fmt.Sprintf("%d of %d running   (unlimited)", linRun, linTotal))

	section("Host")
	pi := foundation.NewProcessInfo()
	row("macOS", pi.OperatingSystemVersionString())
	row("Chip", string(weaveplatform.CurrentArchitecture()))
	row("Memory", formatByteSize(int64(pi.PhysicalMemory())))

	section("Storage")
	if cfg, err := weaveconfig.NewConfig(); err == nil {
		row("Home", cfg.WeaveHomeDir)
	}

	return doc
}

// attrRun builds one styled run for an attributed string.
func attrRun(s string, attrs *foundation.MutableDictionary) *foundation.AttributedString {
	return foundation.NewAttributedStringWithStringAttributes(s, attrs)
}

// fontAttrs returns an attributes dictionary applying font.
func fontAttrs(font *appkit.Font) *foundation.MutableDictionary {
	return textAttrs(font, nil)
}

// textAttrs returns an attributes dictionary applying font and (optionally) a
// foreground color.
func textAttrs(font *appkit.Font, color *appkit.Color) *foundation.MutableDictionary {
	d := foundation.NewMutableDictionary()
	d.Set(appkit.NSFontAttributeName(), font)
	if color != nil {
		d.Set(appkit.NSForegroundColorAttributeName(), color)
	}
	return d
}

// aboutFont resolves an NSFont via a class selector taking a single size (e.g.
// "boldSystemFontOfSize:" or "systemFontOfSize:").
func aboutFont(selector string, size float64) *appkit.Font {
	id := purego.ID(purego.GetClass("NSFont")).Send(purego.RegisterName(selector), size)
	return appkit.FontFromID(id)
}

// brandFont loads the embedded Plus Jakarta Sans face as an in-memory NSFont for
// the wordmark. It builds a CoreText font descriptor from the raw bytes and makes
// an NSFont from it — the face is never registered with the font manager, so it
// is available only to this process for drawing and is not installed into the
// system or user font library. Returns nil if the face can't be loaded.
func brandFont(size float64) *appkit.Font {
	if len(branding.PlusJakartaSansMedium) == 0 {
		return nil
	}
	descriptor := coretext.CTFontManagerCreateFontDescriptorFromData(objcutil.BytesToNSData(branding.PlusJakartaSansMedium))
	if descriptor == nil {
		return nil
	}
	id := purego.ID(purego.GetClass("NSFont")).Send(
		purego.RegisterName("fontWithDescriptor:size:"), obj.ID(descriptor), size)
	if id == 0 {
		return nil
	}
	return appkit.FontFromID(id)
}

// secondaryColor is NSColor.secondaryLabelColor, for the grid's label column.
func secondaryColor() *appkit.Color {
	return appkit.ColorFromID(purego.ID(purego.GetClass("NSColor")).Send(purego.RegisterName("secondaryLabelColor")))
}

// linkColor is NSColor.linkColor, for the repository hyperlink.
func linkColor() *appkit.Color {
	return appkit.ColorFromID(purego.ID(purego.GetClass("NSColor")).Send(purego.RegisterName("linkColor")))
}

// The About window is built once and reused (kept alive by these package vars so
// the Go wrappers aren't finalized); reopening refreshes the dynamic info.
var (
	aboutWindow    *appkit.Window
	aboutInfoField *appkit.TextField
)

// presentAbout shows weave's custom About window: the "guestweave" wordmark in
// the brand font, a divider rule, a two-column info grid, and a repository link.
// (The standard macOS About panel can't rebrand its title font, so this is a
// custom window.) Must run on the main thread.
func presentAbout() {
	const (
		contentW = 400.0
		padH     = 32.0
		innerW   = contentW - 2*padH
		padTop   = 26.0
		padBot   = 22.0
	)

	if aboutWindow != nil {
		aboutInfoField.WithAttributedStringValue(aboutInfoDocument())
		aboutWindow.MakeKeyAndOrderFront(nil)
		return
	}

	wordFont := brandFont(30)
	if wordFont == nil {
		wordFont = aboutFont("systemFontOfSize:", 30)
	}
	wordmark := aboutField(appkit.TextAlignmentCenter)
	wordmark.WithAttributedStringValue(attrRun("guestweave", fontAttrs(wordFont)))

	version := aboutField(appkit.TextAlignmentCenter)
	version.WithAttributedStringValue(attrRun(weaveVersion(), textAttrs(aboutFont("systemFontOfSize:", 11), secondaryColor())))

	divider := appkit.NewBox().WithBoxType(appkit.BoxSeparator)

	info := aboutField(appkit.TextAlignmentLeft)
	info.WithAttributedStringValue(aboutInfoDocument())
	info.WithPreferredMaxLayoutWidth(innerW)
	info.WithFrame(rect(0, 0, innerW, 16))
	obj.ID(info).Send(purego.RegisterName("sizeToFit"))
	hInfo := info.Frame().Size.Height

	// Repository hyperlink: link color + underline so it reads (and behaves) as a
	// clickable link. A bare NSLinkAttributeName renders as plain text in an
	// NSTextField, unlike the standard About panel which styles links itself.
	link := aboutField(appkit.TextAlignmentCenter)
	linkAttrs := textAttrs(aboutFont("systemFontOfSize:", 11), linkColor())
	linkAttrs.Set(appkit.NSLinkAttributeName(), objcutil.NSStr(repoURL))
	linkAttrs.Set(appkit.NSUnderlineStyleAttributeName(), foundation.NewNumberWithInt(int(appkit.UnderlineStyleSingle)))
	link.WithAttributedStringValue(foundation.NewAttributedStringWithStringAttributes(repoURL, linkAttrs))

	const (
		hWord = 38.0
		hVer  = 16.0
		hDiv  = 1.0
		hLink = 16.0
	)
	totalH := padTop + hWord + 2 + hVer + 16 + hDiv + 16 + hInfo + 18 + hLink + padBot

	// Place top-down (AppKit's origin is bottom-left).
	y := totalH - padTop
	y -= hWord
	wordmark.WithFrame(rect(padH, y, innerW, hWord))
	y -= 2 + hVer
	version.WithFrame(rect(padH, y, innerW, hVer))
	y -= 16 + hDiv
	divider.WithFrame(rect(padH, y, innerW, hDiv))
	y -= 16 + hInfo
	info.WithFrame(rect(padH, y, innerW, hInfo))
	y -= 18 + hLink
	link.WithFrame(rect(padH, y, innerW, hLink))

	win := appkit.NewWindowWithContentRectStyleMaskBackingDefer(
		rect(0, 0, contentW, totalH),
		appkit.WindowStyleMaskTitled|appkit.WindowStyleMaskClosable,
		appkit.BackingStoreBuffered, false)
	win.WithReleasedWhenClosed(false)
	win.WithTitle("")

	content := win.ContentView()
	for _, tf := range []*appkit.TextField{wordmark, version, info, link} {
		content.AddSubview(appkit.ViewFromID(obj.ID(tf)))
	}
	content.AddSubview(appkit.ViewFromID(obj.ID(divider)))

	win.Center()
	win.MakeKeyAndOrderFront(nil)
	aboutWindow = win
	aboutInfoField = info
}

// aboutField builds a read-only, selectable, multi-line, borderless text field
// used for each piece of the About window.
func aboutField(align appkit.TextAlignment) *appkit.TextField {
	tf := appkit.TextFieldFromID(objcutil.AllocClass("NSTextField").Send(purego.RegisterName("init")))
	tf.WithSelectable(true)
	tf.WithEditable(false)
	tf.WithBordered(false)
	tf.WithDrawsBackground(false)
	tf.WithUsesSingleLineMode(false)
	tf.WithMaximumNumberOfLines(0)
	tf.WithAlignment(align)
	return tf
}

func rect(x, y, w, h float64) corefoundation.CGRect {
	return corefoundation.CGRect{Origin: corefoundation.CGPoint{X: x, Y: y}, Size: corefoundation.CGSize{Width: w, Height: h}}
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
