// Package ui hosts the graphical VM window for `weave run`, porting tart's
// SwiftUI MainApp/VMView/AppDelegate (Sources/tart/Commands/Run.swift) onto raw
// AppKit: an NSWindow wrapping a VZVirtualMachineView, plus the application and
// Control menus.
//go:build darwin

package ui

import (
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/deploymenttheory/weave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	weavevm "github.com/deploymenttheory/weave/internal/vm"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
	appkit "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/appkit"
	corefoundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/corefoundation"
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
)

// Window hosts a running VM's VZVirtualMachineView in an AppKit window with the
// application and Control menus.
type Window struct {
	VM                *weavevm.VM
	CaptureSystemKeys bool
	Suspendable       bool
	// VMDir is the VM bundle directory, used by Reveal in Finder and guest-IP
	// resolution (control socket).
	VMDir string
}

// activeVM and suspendableFlag back the window delegate and menu target, whose
// ObjC callbacks cannot carry Go closure state. weave runs a single VM per
// process, so this package-level state mirrors tart's global `vm` and
// MainApp.suspendable.
var (
	activeVM        *weavevm.VM
	suspendableFlag atomic.Bool
)

// RevertFunc, when set by the run command, performs an in-process snapshot
// revert and reports whether it was handled in place (false ⇒ the caller should
// fall back to the relaunch path). It lets the UI trigger a revert without the
// ui package importing internal/command (which would be an import cycle).
var RevertFunc func(ref string) bool

// SwapVM re-points the run window at a rebuilt VM after an in-process snapshot
// revert. VZVirtualMachineView.WithVirtualMachine auto-dispatches onto the main
// thread in the SDK, so this is safe to call from the run loop's goroutine.
func SwapVM(newVM *weavevm.VM) {
	activeVM = newVM
	if activeView != nil {
		activeView.WithVirtualMachine(newVM.VirtualMachine)
	}
}

// Run ports Run.runUI/MainApp: it installs the menu bar, builds the window, and
// enters the AppKit run loop. It blocks until the application terminates.
func (w *Window) Run() {
	activeVM = w.VM
	activeVMDir = w.VMDir
	suspendableFlag.Store(w.Suspendable)

	app := appkit.SharedApplication()
	app.SetActivationPolicy(appkit.ApplicationActivationPolicyRegular)
	setAppDisplayName("guestweave")
	installMainMenu(app)

	contentRect := corefoundation.CGRect{
		Origin: corefoundation.CGPoint{X: 0, Y: 0},
		Size: corefoundation.CGSize{
			Width:  float64(w.VM.Config.Display.Width),
			Height: float64(w.VM.Config.Display.Height),
		},
	}
	styleMask := appkit.WindowStyleMaskTitled | appkit.WindowStyleMaskClosable |
		appkit.WindowStyleMaskMiniaturizable | appkit.WindowStyleMaskResizable

	window := appkit.NewWindowWithContentRectStyleMaskBackingDefer(
		contentRect, styleMask, appkit.BackingStoreBuffered, false)
	window.WithTitle(w.VM.Name)

	machineView := virtualization.VirtualMachineViewFromID(
		purego.Send[purego.ID](objcutil.AllocClass("VZVirtualMachineView"), purego.RegisterName("init")))
	activeView = machineView
	machineView.WithCapturesSystemKeys(w.CaptureSystemKeys)

	// If not specified, enable automatic display reconfiguration for guests
	// that support it. Disabled for Linux because of poor HiDPI support.
	displayRefit := w.VM.Config.OS != weaveplatform.OSLinux
	if w.VM.Config.DisplayRefit != nil {
		displayRefit = *w.VM.Config.DisplayRefit
	}
	if weaveplatform.MacOSAtLeast(14) && displayRefit {
		machineView.WithAutomaticallyReconfiguresDisplay(true)
	}
	machineView.WithVirtualMachine(w.VM.VirtualMachine)

	window.WithContentView(appkit.ViewFromID(obj.ID(machineView)))
	// NSWindow's delegate is a runtime class (target/action), set by hand.
	obj.ID(window).Send(purego.RegisterName("setDelegate:"),
		purego.ID(windowDelegateClass()).Send(purego.RegisterName("new")))
	window.Center()
	window.MakeKeyAndOrderFront(nil)

	app.ActivateIgnoringOtherApps(true)
	app.Run()
}

// setAppDisplayName forces the application's display name — the bold app-menu
// title, the Dock tile, and the Force-Quit list — to the product identity,
// independent of how the binary was launched. Without it macOS uses the process
// name (argv[0]), which the Restart and snapshot-revert paths re-exec through
// `/bin/sh -c`, leaving it as something like "weave snapshot". Setting it on the
// shared NSProcessInfo before the menu bar is installed makes the app menu read
// the correct name.
func setAppDisplayName(name string) {
	pi := purego.ID(purego.GetClass("NSProcessInfo")).Send(purego.RegisterName("processInfo"))
	pi.Send(purego.RegisterName("setProcessName:"), obj.ID(objcutil.NSStr(name)))
}

// windowDelegateClass registers the window delegate that translates a window
// close into SIGUSR1 (suspendable) or SIGINT, mirroring MainApp's onDisappear
// handler.
var windowDelegateClass = sync.OnceValue(func() purego.Class {
	class, err := purego.RegisterClass("OrinRunWindowDelegate", purego.GetClass("NSObject"),
		[]*purego.Protocol{purego.GetProtocol("NSWindowDelegate")},
		nil,
		[]purego.MethodDef{
			{
				Cmd: purego.RegisterName("windowWillClose:"),
				Fn: func(_ purego.ID, _ purego.SEL, _ purego.ID) {
					signum := syscall.SIGINT
					if suspendableFlag.Load() {
						signum = syscall.SIGUSR1
					}
					_ = syscall.Kill(syscall.Getpid(), signum)
				},
			},
		})
	if err != nil {
		panic(fmt.Sprintf("failed to register OrinRunWindowDelegate: %v", err))
	}
	return class
})

// installMainMenu builds the menu bar for the VM window. tart's SwiftUI MainApp
// (Run.swift:838) inherits the full standard macOS menu bar for free and only
// trims a few groups (Help, File>New, Edit pasteboard/textEditing/undoRedo,
// Window sizing) via `.commands { }`, keeping the App, View (Enter Full Screen),
// Window (Minimize/Zoom) and a custom Control menu. Raw AppKit starts with no
// menu at all, so we rebuild that effective menu bar by hand: App (custom About,
// Services, Hide/Hide Others/Show All, custom Quit), View, Control, Window.
// newBlankMenuItem creates an empty NSMenuItem, used as the carrier for a
// submenu. The plain title/action/key constructor takes an Objective-C selector
// for the action, which the idiomatic binding does not express, so the item is
// allocated directly.
func newBlankMenuItem() *appkit.MenuItem {
	return appkit.MenuItemFromID(objcutil.AllocClass("NSMenuItem").Send(purego.RegisterName("init")))
}

// addMenuItemWithAction appends a menu item whose action is an Objective-C
// selector — the target/action wiring that drives every command and is not
// expressible idiomatically — and returns the item as an idiomatic wrapper. A
// blank selector leaves the action nil (a plain separator-free label).
func addMenuItemWithAction(menu *appkit.Menu, title, selector, keyEquivalent string) *appkit.MenuItem {
	var action purego.SEL
	if selector != "" {
		action = purego.RegisterName(selector)
	}
	itemID := obj.ID(menu).Send(
		purego.RegisterName("addItemWithTitle:action:keyEquivalent:"),
		obj.ID(objcutil.NSStr(title)), action, obj.ID(objcutil.NSStr(keyEquivalent)))
	return appkit.MenuItemFromID(itemID)
}

func installMainMenu(app *appkit.Application) {
	target := purego.ID(menuTargetClass()).Send(purego.RegisterName("new"))

	newMenu := func(title string) *appkit.Menu {
		return appkit.NewMenuWithTitle(title)
	}
	newSubmenuItem := newBlankMenuItem
	// addItem adds an item routed to weave's custom action target (About, the
	// Control verbs, Quit).
	addItem := func(menu *appkit.Menu, title, selector, keyEquivalent string) *appkit.MenuItem {
		item := addMenuItemWithAction(menu, title, selector, keyEquivalent)
		item.WithTarget(obj.Wrap(target))
		return item
	}
	// addStd adds a standard AppKit item with a nil target, so the action travels
	// the responder chain to NSApp / the key window (Hide, Minimize, Full Screen…).
	addStd := func(menu *appkit.Menu, title, selector, keyEquivalent string) *appkit.MenuItem {
		return addMenuItemWithAction(menu, title, selector, keyEquivalent)
	}
	// addTopMenu attaches a titled submenu under the main menu bar.
	mainMenu := newMenu("")
	addTopMenu := func(title string) *appkit.Menu {
		item := newSubmenuItem()
		mainMenu.AddItem(item)
		sub := newMenu(title)
		mainMenu.SetSubmenuForItem(sub, item)
		return sub
	}

	// ── Application menu. The first submenu is always rendered as the app menu
	// regardless of its title (the displayed name comes from the process).
	appMenu := addTopMenu("")
	addItem(appMenu, "About Weave", "weaveAbout:", "")
	appMenu.AddItem(appkit.SeparatorItem())
	// Services submenu — macOS populates and manages it once registered.
	servicesItem := addMenuItemWithAction(appMenu, "Services", "", "")
	servicesMenu := newMenu("Services")
	servicesItem.WithSubmenu(servicesMenu)
	app.WithServicesMenu(servicesMenu)
	appMenu.AddItem(appkit.SeparatorItem())
	addStd(appMenu, "Hide Weave", "hide:", "h")
	hideOthers := addStd(appMenu, "Hide Others", "hideOtherApplications:", "h")
	hideOthers.WithKeyEquivalentModifierMask(appkit.EventModifierFlagOption | appkit.EventModifierFlagCommand)
	addStd(appMenu, "Show All", "unhideAllApplications:", "")
	appMenu.AddItem(appkit.SeparatorItem())
	addItem(appMenu, "VM Info…", "weaveVMInfo:", "")
	appMenu.AddItem(appkit.SeparatorItem())
	// Quit honours suspend-on-close, so it routes to the custom target.
	addItem(appMenu, "Quit Weave", "weaveQuit:", "q")

	// ── File menu: disk / inspection.
	fileMenu := addTopMenu("File")
	addItem(fileMenu, "Reveal in Finder", "weaveReveal:", "")
	addItem(fileMenu, "Show Logs", "weaveLogs:", "")
	addItem(fileMenu, "Clear Logs…", "weaveClearLogs:", "")

	// ── Connect menu: guest access.
	connectMenu := addTopMenu("Connect")
	addItem(connectMenu, "SSH in Terminal", "weaveSSH:", "")
	addItem(connectMenu, "Open Guest Shell", "weaveShell:", "")
	connectMenu.AddItem(appkit.SeparatorItem())
	addItem(connectMenu, "Open VNC Viewer", "weaveVNC:", "")
	addItem(connectMenu, "Copy IP Address", "weaveCopyIP:", "")

	// ── View menu: full-screen toggle (AppKit auto-swaps Enter/Exit on the title).
	viewMenu := addTopMenu("View")
	fullScreen := addStd(viewMenu, "Enter Full Screen", "toggleFullScreen:", "f")
	fullScreen.WithKeyEquivalentModifierMask(appkit.EventModifierFlagControl | appkit.EventModifierFlagCommand)
	viewMenu.AddItem(appkit.SeparatorItem())
	addItem(viewMenu, "Take Screenshot", "weaveScreenshot:", "s")
	addItem(viewMenu, "Toggle Screen Share", "weaveScreenShare:", "")

	// ── Control menu (Run.swift:848): Start / Stop / Request Stop, plus Suspend
	// (snapshot the VM to disk). Always offered on macOS 14+: the handler
	// snapshots when the VM is running suspendable, otherwise offers to relaunch
	// it that way (a VM not started suspendable can't be snapshotted live).
	controlMenu := addTopMenu("Control")
	addItem(controlMenu, "Start", "weaveStart:", "")
	addItem(controlMenu, "Stop", "weaveStop:", "")
	addItem(controlMenu, "Request Stop", "weaveRequestStop:", "")
	if weaveplatform.MacOSAtLeast(14) {
		addItem(controlMenu, "Suspend (RAM State)…", "weaveSuspend:", "")
	}
	// Disk snapshots: a submenu to take, revert to, and delete named snapshots.
	// Available on any macOS version (file clone + VZ pause/resume).
	snapshotsItem := addMenuItemWithAction(controlMenu, "Snapshots", "", "")
	snapshotsMenu := newMenu("Snapshots")
	snapshotsItem.WithSubmenu(snapshotsMenu)
	addItem(snapshotsMenu, "Take Snapshot…", "weaveSnapshot:", "")
	snapshotsMenu.AddItem(appkit.SeparatorItem())
	addItem(snapshotsMenu, "Revert to Snapshot…", "weaveSnapshotRevert:", "")
	addItem(snapshotsMenu, "Delete Snapshot…", "weaveSnapshotDelete:", "")
	controlMenu.AddItem(appkit.SeparatorItem())
	addItem(controlMenu, "Restart", "weaveRestart:", "")
	addItem(controlMenu, "Force Stop", "weaveForceStop:", "")
	controlMenu.AddItem(appkit.SeparatorItem())
	addItem(controlMenu, "Clipboard Status…", "weaveClipboard:", "")

	// ── Window menu: standard window management; AppKit appends "Bring All to
	// Front" and the live window list once registered as the windows menu.
	windowMenu := addTopMenu("Window")
	addStd(windowMenu, "Minimize", "performMiniaturize:", "m")
	addStd(windowMenu, "Zoom", "performZoom:", "")
	app.WithWindowsMenu(windowMenu)

	app.WithMainMenu(mainMenu)
}

// menuTargetClass registers the menu action target. Each Control menu item
// routes through the signal handlers installed in RunMainThread (so it reuses
// the existing stop/suspend machinery), except Start which has no handler and
// calls into the VM directly. Mirrors the registration of windowDelegateClass.
var menuTargetClass = sync.OnceValue(func() purego.Class {
	killSelf := func(sig syscall.Signal) { _ = syscall.Kill(syscall.Getpid(), sig) }
	method := func(name string, fn func()) purego.MethodDef {
		return purego.MethodDef{
			Cmd: purego.RegisterName(name),
			Fn: func(_ purego.ID, _ purego.SEL, _ purego.ID) {
				fn()
			},
		}
	}

	class, err := purego.RegisterClass("WeaveRunMenuTarget", purego.GetClass("NSObject"), nil, nil,
		[]purego.MethodDef{
			method("weaveAbout:", showAboutPanel),
			// Run.swift:849 calls vm.virtualMachine.start(); StartMachine must run
			// off the main thread because it blocks on a main-queue completion.
			method("weaveStart:", func() { go func() { _ = activeVM.StartMachine(false) }() }),
			method("weaveStop:", func() { killSelf(syscall.SIGINT) }),
			method("weaveRequestStop:", func() { killSelf(syscall.SIGUSR2) }),
			method("weaveSuspend:", suspendFromMenu),
			method("weaveSnapshot:", takeSnapshotFromMenu),
			method("weaveSnapshotRevert:", revertSnapshotFromMenu),
			method("weaveSnapshotDelete:", deleteSnapshotFromMenu),
			method("weaveForceStop:", forceStop),
			method("weaveRestart:", restartVM),
			method("weaveClipboard:", showClipboardStatus),
			method("weaveVMInfo:", showVMInfo),
			method("weaveReveal:", revealInFinder),
			method("weaveLogs:", showLogs),
			method("weaveClearLogs:", clearLogs),
			method("weaveSSH:", connectSSH),
			method("weaveShell:", openGuestShell),
			method("weaveVNC:", openVNCViewer),
			method("weaveCopyIP:", copyIPAddress),
			method("weaveScreenshot:", takeScreenshot),
			method("weaveScreenShare:", toggleScreenShare),
			// Quit honors suspend-on-close like MainApp's onDisappear (Run.swift:822).
			method("weaveQuit:", func() {
				if suspendableFlag.Load() {
					killSelf(syscall.SIGUSR1)
				} else {
					killSelf(syscall.SIGINT)
				}
			}),
		})
	if err != nil {
		panic(fmt.Sprintf("failed to register WeaveRunMenuTarget: %v", err))
	}
	return class
})

// showAboutPanel shows the standard macOS About panel for weave: the panel draws
// the app icon, name, and version, and aboutCredits supplies a typeset summary
// of global/host/runtime facts as the credits. Per-VM configuration lives in VM
// Info, not here.
func showAboutPanel() {
	app := appkit.SharedApplication()

	options := foundation.NewMutableDictionary()
	options.Set(appkit.NSAboutPanelOptionApplicationName(), objcutil.NSStr("guestweave"))
	options.Set(appkit.NSAboutPanelOptionApplicationVersion(), objcutil.NSStr(weaveVersion()))
	options.Set(appkit.NSAboutPanelOptionCredits(), aboutCredits())

	obj.ID(app).Send(purego.RegisterName("orderFrontStandardAboutPanelWithOptions:"), obj.ID(options))
}

// RunHeadless enters the AppKit run loop without bringing up a window, waiting
// for the VM to exit. Ports Run.swift's no-UI path (used by --no-graphics and
// VNC-only runs): NSApplication.setActivationPolicy(.prohibited) + run().
func RunHeadless() {
	app := appkit.SharedApplication()
	app.SetActivationPolicy(appkit.ApplicationActivationPolicyProhibited)
	app.Run()
}

// OpenURL opens url with the user's default handler via NSWorkspace (e.g. the
// system VNC viewer for a vnc:// URL).
func OpenURL(url string) {
	appkit.SharedWorkspace().OpenURL(url)
}
