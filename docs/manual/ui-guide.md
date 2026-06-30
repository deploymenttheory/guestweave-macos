# UI Guide

`guestweave run <name>` (without `--no-graphics`) opens a native AppKit window
showing the guest's screen, plus a full menu bar. The window owns the foreground
process: closing it stops (or suspends) the VM, and the menus drive the live VM.

```sh
weave run my-vm                 # graphical window
weave run my-vm --no-graphics   # no window (headless)
weave run my-vm --vnc           # no native window; serve a VNC endpoint
```

## The window

- The content is a live `VZVirtualMachineView` of the guest's framebuffer.
- Resize freely; with `--capture-system-keys`, system combos (e.g. Cmd-Tab) go
  to the guest instead of the host.
- Closing the window terminates the run — gracefully, honouring `--suspendable`
  (it suspends instead of stopping when suspend-on-close applies).

## Menu bar

### Weave (application menu)
| Item | Shortcut | Action |
|------|----------|--------|
| About Weave | | Panel with the VM's CPU / memory / display + project link |
| Services ▸ | | Standard macOS Services submenu |
| Hide Weave | ⌘H | Hide the app |
| Hide Others | ⌥⌘H | Hide other apps |
| Show All | | Unhide all |
| VM Info… | | Dialog with name, OS, CPU, memory, display, directory |
| Quit Weave | ⌘Q | Quit (honours suspend-on-close) |

### File
| Item | Action |
|------|--------|
| Reveal in Finder | Open the VM's bundle directory in Finder |
| Show Logs | Open the error log in the default viewer (Console) |
| Clear Logs… | Delete all log files after confirmation (same as `guestweave logs clear`) — see [Logging](logging.md) |

### Connect (guest access)
| Item | Action |
|------|--------|
| SSH in Terminal | Opens Terminal running `guestweave ssh <name>` |
| Open Guest Shell | Opens Terminal running `guestweave exec -it <name> /bin/sh` (vsock agent; macOS 14+) |
| Open VNC Viewer | Opens the VM's VNC URL (only when started with `--vnc`) |
| Copy IP Address | Resolves the guest IP and copies it to the clipboard |

### View
| Item | Shortcut | Action |
|------|----------|--------|
| Enter Full Screen | ⌃⌘F | Toggle full-screen |
| Take Screenshot | ⌘S | Save a PNG of the guest screen to `~/Desktop` |
| Toggle Screen Share | | Start/stop a view-only browser stream (needs `--vnc`) |

### Control
| Item | Action |
|------|--------|
| Start | Start the VM |
| Stop | Power off (immediate) |
| Request Stop | Request a graceful guest shutdown (ACPI) |
| Suspend | Save state (macOS 14+, with `--suspendable`) |
| Restart | Power off and relaunch with the same options |
| Force Stop | Terminate immediately without a clean shutdown (confirmation; possible data loss) |
| Clipboard Status… | Show the resolved clipboard sync direction |

### Window
| Item | Shortcut | Action |
|------|----------|--------|
| Minimize | ⌘M | Minimize the window |
| Zoom | | Zoom the window |
| (window list) | | AppKit-managed list of open windows |

## Notes & caveats

- **SSH / Open Guest Shell** launch Terminal via a temporary `.command` script
  (no Automation-permission prompt). SSH uses the default `weave`/`weave`
  credentials; override at the CLI with `guestweave ssh --user … --password …`.
- **Open VNC Viewer / Toggle Screen Share** require the VM to have been started
  with `--vnc` (otherwise they report that VNC is not enabled).
- **Take Screenshot** captures the AppKit view; on some Metal/IOSurface-backed
  guest framebuffers the capture may be blank — use the VNC stream as the
  reliable source in that case.
- **Restart** gracefully stops the current run and relaunches `guestweave run`
  with the same arguments (so VNC, directory shares, etc. are preserved); the
  window reappears after a few seconds.
- **Clipboard Status** is read-only here; change a running VM's policy live with
  `weave clipboard set` — see [Clipboard](clipboard.md).
