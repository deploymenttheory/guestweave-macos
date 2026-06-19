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

	"github.com/deploymenttheory/weave/internal/ci"
	"github.com/deploymenttheory/weave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	weavevm "github.com/deploymenttheory/weave/internal/vm"

	appkit "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/appkit"
	corefoundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/corefoundation"
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/virtualization"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
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

// Run ports Run.runUI/MainApp: it installs the menu bar, builds the window, and
// enters the AppKit run loop. It blocks until the application terminates.
func (w *Window) Run() {
	activeVM = w.VM
	activeVMDir = w.VMDir
	suspendableFlag.Store(w.Suspendable)

	app := appkit.SharedApplication()
	app.SetActivationPolicy(appkit.NSApplicationActivationPolicyRegular)
	installMainMenu(app, w.Suspendable)

	contentRect := corefoundation.CGRect{
		Origin: corefoundation.CGPoint{X: 0, Y: 0},
		Size: corefoundation.CGSize{
			Width:  float64(w.VM.Config.Display.Width),
			Height: float64(w.VM.Config.Display.Height),
		},
	}
	styleMask := appkit.NSWindowStyleMaskTitled | appkit.NSWindowStyleMaskClosable |
		appkit.NSWindowStyleMaskMiniaturizable | appkit.NSWindowStyleMaskResizable

	window := appkit.NewWindowWithContentRectStyleMaskBackingDefer(
		contentRect, styleMask, appkit.NSBackingStoreBuffered, false)
	window.SetTitle(w.VM.Name)

	machineView := virtualization.VirtualMachineViewFromID(
		purego.Send[purego.ID](objcutil.AllocClass("VZVirtualMachineView"), purego.RegisterName("init")))
	activeView = machineView
	machineView.SetCapturesSystemKeys(w.CaptureSystemKeys)

	// If not specified, enable automatic display reconfiguration for guests
	// that support it. Disabled for Linux because of poor HiDPI support.
	displayRefit := w.VM.Config.OS != weaveplatform.OSLinux
	if w.VM.Config.DisplayRefit != nil {
		displayRefit = *w.VM.Config.DisplayRefit
	}
	if weaveplatform.MacOSAtLeast(14) && displayRefit {
		machineView.SetAutomaticallyReconfiguresDisplay(true)
	}
	machineView.SetVirtualMachine(w.VM.VirtualMachine.Unwrap())

	window.SetContentView(&machineView.Unwrap().NSView)
	window.SetDelegate(purego.ID(windowDelegateClass()).Send(purego.RegisterName("new")))
	window.Center()
	window.MakeKeyAndOrderFront(0)

	app.ActivateIgnoringOtherApps(true)
	app.Run()
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
func installMainMenu(app *appkit.Application, suspendable bool) {
	target := purego.ID(menuTargetClass()).Send(purego.RegisterName("new"))

	newMenu := func(title string) *appkit.Menu {
		return appkit.NewMenuWithTitle(title)
	}
	newSubmenuItem := func() *appkit.MenuItem {
		return appkit.NewMenuItemWithTitleActionKeyEquivalent("", 0, "")
	}
	// addItem adds an item routed to weave's custom action target (About, the
	// Control verbs, Quit).
	addItem := func(menu *appkit.Menu, title, selector, keyEquivalent string) *appkit.MenuItem {
		item := menu.AddItemWithTitleActionKeyEquivalent(
			title, purego.RegisterName(selector), keyEquivalent)
		item.SetTarget(target)
		return item
	}
	// addStd adds a standard AppKit item with a nil target, so the action travels
	// the responder chain to NSApp / the key window (Hide, Minimize, Full Screen…).
	addStd := func(menu *appkit.Menu, title, selector, keyEquivalent string) *appkit.MenuItem {
		return menu.AddItemWithTitleActionKeyEquivalent(
			title, purego.RegisterName(selector), keyEquivalent)
	}
	// addTopMenu attaches a titled submenu under the main menu bar.
	mainMenu := newMenu("")
	addTopMenu := func(title string) *appkit.Menu {
		item := newSubmenuItem()
		mainMenu.AddItem(item.Unwrap())
		sub := newMenu(title)
		mainMenu.SetSubmenuForItem(sub.Unwrap(), item.Unwrap())
		return sub
	}

	// ── Application menu. The first submenu is always rendered as the app menu
	// regardless of its title (the displayed name comes from the process).
	appMenu := addTopMenu("")
	addItem(appMenu, "About Weave", "weaveAbout:", "")
	appMenu.AddItem(appkit.SeparatorItem().Unwrap())
	// Services submenu — macOS populates and manages it once registered.
	servicesItem := appMenu.AddItemWithTitleActionKeyEquivalent("Services", 0, "")
	servicesMenu := newMenu("Services")
	servicesItem.SetSubmenu(servicesMenu.Unwrap())
	app.SetServicesMenu(servicesMenu.Unwrap())
	appMenu.AddItem(appkit.SeparatorItem().Unwrap())
	addStd(appMenu, "Hide Weave", "hide:", "h")
	hideOthers := addStd(appMenu, "Hide Others", "hideOtherApplications:", "h")
	hideOthers.SetKeyEquivalentModifierMask(appkit.NSEventModifierFlagOption | appkit.NSEventModifierFlagCommand)
	addStd(appMenu, "Show All", "unhideAllApplications:", "")
	appMenu.AddItem(appkit.SeparatorItem().Unwrap())
	addItem(appMenu, "VM Info…", "weaveVMInfo:", "")
	appMenu.AddItem(appkit.SeparatorItem().Unwrap())
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
	connectMenu.AddItem(appkit.SeparatorItem().Unwrap())
	addItem(connectMenu, "Open VNC Viewer", "weaveVNC:", "")
	addItem(connectMenu, "Copy IP Address", "weaveCopyIP:", "")

	// ── View menu: full-screen toggle (AppKit auto-swaps Enter/Exit on the title).
	viewMenu := addTopMenu("View")
	fullScreen := addStd(viewMenu, "Enter Full Screen", "toggleFullScreen:", "f")
	fullScreen.SetKeyEquivalentModifierMask(appkit.NSEventModifierFlagControl | appkit.NSEventModifierFlagCommand)
	viewMenu.AddItem(appkit.SeparatorItem().Unwrap())
	addItem(viewMenu, "Take Screenshot", "weaveScreenshot:", "s")
	addItem(viewMenu, "Toggle Screen Share", "weaveScreenShare:", "")

	// ── Control menu (Run.swift:848): Start / Stop / Request Stop, plus Suspend
	// when the VM is suspendable on macOS 14+.
	controlMenu := addTopMenu("Control")
	addItem(controlMenu, "Start", "weaveStart:", "")
	addItem(controlMenu, "Stop", "weaveStop:", "")
	addItem(controlMenu, "Request Stop", "weaveRequestStop:", "")
	if weaveplatform.MacOSAtLeast(14) && suspendable {
		addItem(controlMenu, "Suspend", "weaveSuspend:", "")
	}
	controlMenu.AddItem(appkit.SeparatorItem().Unwrap())
	addItem(controlMenu, "Restart", "weaveRestart:", "")
	addItem(controlMenu, "Force Stop", "weaveForceStop:", "")
	controlMenu.AddItem(appkit.SeparatorItem().Unwrap())
	addItem(controlMenu, "Clipboard Status…", "weaveClipboard:", "")

	// ── Window menu: standard window management; AppKit appends "Bring All to
	// Front" and the live window list once registered as the windows menu.
	windowMenu := addTopMenu("Window")
	addStd(windowMenu, "Minimize", "performMiniaturize:", "m")
	addStd(windowMenu, "Zoom", "performZoom:", "")
	app.SetWindowsMenu(windowMenu.Unwrap())

	app.SetMainMenu(mainMenu.Unwrap())
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
			method("weaveSuspend:", func() { killSelf(syscall.SIGUSR1) }),
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

// showAboutPanel ports AboutTart (Run.swift:886): an About panel listing the
// VM's CPU, memory and display plus a link to the weave repository.
func showAboutPanel() {
	app := appkit.SharedApplication()

	credits := fmt.Sprintf("CPU: %d cores\nMemory: %d MB\nDisplay: %s\nhttps://github.com/deploymenttheory/guestweave-macos",
		activeVM.Config.CPUCount, activeVM.Config.MemorySize/1024/1024, activeVM.Config.Display.String())
	attributedCredits := purego.Send[purego.ID](objcutil.AllocClass("NSAttributedString"),
		purego.RegisterName("initWithString:"), objcutil.NSStr(credits).ID())

	// The idiomatic appkit option-key accessors return the NSString objc.ID
	// directly (no symbol-address dereference needed); build the options map with
	// the idiomatic MutableDictionary and send the panel request via the runtime.
	options := foundation.NewMutableDictionary()
	options.Set(appkit.NSAboutPanelOptionApplicationName(), objcutil.NSStr("Weave").ID())
	options.Set(appkit.NSAboutPanelOptionApplicationVersion(), objcutil.NSStr(ci.CIVersion()).ID())
	options.Set(appkit.NSAboutPanelOptionCredits(), attributedCredits)

	purego.Send[purego.ID](app.ID(), purego.RegisterName("orderFrontStandardAboutPanelWithOptions:"), options.ID())
}

// RunHeadless enters the AppKit run loop without bringing up a window, waiting
// for the VM to exit. Ports Run.swift's no-UI path (used by --no-graphics and
// VNC-only runs): NSApplication.setActivationPolicy(.prohibited) + run().
func RunHeadless() {
	app := appkit.SharedApplication()
	app.SetActivationPolicy(appkit.NSApplicationActivationPolicyProhibited)
	app.Run()
}

// OpenURL opens url with the user's default handler via NSWorkspace (e.g. the
// system VNC viewer for a vnc:// URL).
func OpenURL(url string) {
	appkit.SharedWorkspace().OpenURL(url)
}
