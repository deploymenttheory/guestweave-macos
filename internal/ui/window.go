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

// installMainMenu builds the menu bar for the VM window, porting MainApp's
// `.commands { }` block (Run.swift:838). SwiftUI auto-generated a full menu bar
// that tart then trimmed; raw AppKit starts with no menu at all, so there is
// nothing to remove — we add only the application menu (custom About + Quit)
// and the Control menu.
func installMainMenu(app *appkit.Application, suspendable bool) {
	target := purego.ID(menuTargetClass()).Send(purego.RegisterName("new"))

	newMenu := func(title string) *appkit.Menu {
		return appkit.NewMenuWithTitle(title)
	}
	newSubmenuItem := func() *appkit.MenuItem {
		return appkit.NewMenuItemWithTitleActionKeyEquivalent("", 0, "")
	}
	addItem := func(menu *appkit.Menu, title, selector, keyEquivalent string) {
		item := menu.AddItemWithTitleActionKeyEquivalent(
			title, purego.RegisterName(selector), keyEquivalent)
		item.SetTarget(target)
	}

	mainMenu := newMenu("")

	// The first submenu is always rendered as the application menu, regardless
	// of its title (the displayed name comes from the process).
	appItem := newSubmenuItem()
	mainMenu.AddItem(appItem.Unwrap())
	appMenu := newMenu("")
	addItem(appMenu, "About Weave", "weaveAbout:", "")
	appMenu.AddItem(appkit.SeparatorItem().Unwrap())
	addItem(appMenu, "Quit Weave", "weaveQuit:", "q")
	mainMenu.SetSubmenuForItem(appMenu.Unwrap(), appItem.Unwrap())

	// Control menu (Run.swift:848): Start / Stop / Request Stop, plus Suspend
	// when the VM is suspendable on macOS 14+.
	controlItem := newSubmenuItem()
	mainMenu.AddItem(controlItem.Unwrap())
	controlMenu := newMenu("Control")
	addItem(controlMenu, "Start", "weaveStart:", "")
	addItem(controlMenu, "Stop", "weaveStop:", "")
	addItem(controlMenu, "Request Stop", "weaveRequestStop:", "")
	if weaveplatform.MacOSAtLeast(14) && suspendable {
		addItem(controlMenu, "Suspend", "weaveSuspend:", "")
	}
	mainMenu.SetSubmenuForItem(controlMenu.Unwrap(), controlItem.Unwrap())

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

	credits := fmt.Sprintf("CPU: %d cores\nMemory: %d MB\nDisplay: %s\nhttps://github.com/deploymenttheory/weave",
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
