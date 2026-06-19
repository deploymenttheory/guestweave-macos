# guestweave `run` UI — Menu Map (for review)

Maps the *current* run-window menu bar plus *proposed* additions drawn from the
`internal/command` package. Review each option by its tag and decide what to keep.

## Legend
- `[NOW]`   already implemented (tart parity, shipped)
- `[T1]`    proposed — Tier 1: high value, low risk, fits a live VM, idiomatic
- `[T2]`    proposed — Tier 2: valuable, more effort or narrower
- `[T3]`    proposed — Tier 3: deferred to a separate multi-VM "Manager" window
- `(src)`   backing `command`-package capability · `{in-proc}` or `{subproc}` wiring

---

## Menu bar (left → right)

```
 Weave        File*        Connect*       View         Control        Window
```
`*` = new top-level menu.

---

### 🅐  Weave  (application menu)
```
 About Weave .......................... [NOW]
 ──────────
 Services ▸ ........................... [NOW]
 ──────────
 Hide Weave                    ⌘H ..... [NOW]
 Hide Others                  ⌥⌘H ..... [NOW]
 Show All ............................. [NOW]
 ──────────
 VM Info… ............................. [T2]  (GetCommand) {in-proc} richer than About
 ──────────
 Quit Weave                    ⌘Q ..... [NOW]
```

### 🅑  File*  (new — disk/inspection)
```
 Reveal in Finder ..................... [T1]  (VM dir) {in-proc} Workspace.SelectFileInFileViewerRootedAtPath
 Show Logs ............................ [T1]  (LogsCommand) {subproc/Console} open log file
 ──────────
 Export VM… ........................... [T3]  (ExportCommand) needs VM stopped — Manager window
 Import VM… ........................... [T3]  (ImportCommand)                  — Manager window
```

### 🅒  Connect*  (new — guest access)
```
 SSH in Terminal ...................... [T1]  (SSHCommand)  {subproc} open -a Terminal → guestweave ssh
 Open Guest Shell (exec) .............. [T2]  (ExecCommand) {subproc} vsock agent; macOS 14+, agent req'd
 ──────────
 Open VNC Viewer ...................... [T1]  (ui.OpenURL + VNC URL) {in-proc} only when --vnc
 Copy IP Address ...................... [T1]  (IPCommand + macaddress) {in-proc} → clipboard
```

### 🅓  View
```
 Enter Full Screen            ⌃⌘F ..... [NOW]
 ──────────
 Take Screenshot               ⌘S ..... [T1]  (VZVirtualMachineView) {in-proc} View.DataWithPDFInsideRect → file
 Toggle Screen Share (view-only) ...... [T2]  (screenviewer / --show-screen) {in-proc} starts MJPEG server
```

### 🅔  Control
```
 Start ................................ [NOW]
 Stop ................................. [NOW]
 Request Stop ......................... [NOW]
 Suspend .............................. [NOW]  (macOS 14+ & --suspendable)
 ──────────
 Force Stop ........................... [T2]  {in-proc} vm.Stop hard — data-loss risk
 Restart .............................. [T2]  {in-proc} stop+start
 ──────────
 Clipboard ▸ .......................... [T2]  (clipboard.Engine) status + enable/disable; policy fixed at launch
```

### 🅕  Window
```
 Minimize                      ⌘M ..... [NOW]
 Zoom ................................. [NOW]
 ──────────
 (live window list) ................... [NOW]  (AppKit auto via SetWindowsMenu)
```

---

## 🅖  Deferred — separate "VM Manager" window  [T3]
Not part of the single-VM run window; a future standalone scene (table of all VMs + background jobs).
Overlaps the CLI / HTTP API, so explicitly deferred:
```
 List / table of all VMs .............. (ListCommand)
 Create… / Clone… / Rename… / Delete .. (Create/Clone/Rename/Delete Command)
 Export… / Import… / Push… / Pull… ..... (Export/Import/Push/Pull Command)
 Settings… (CPU / Mem / Display / Disk)  (SetCommand) — requires VM stopped
 Login / Logout (registry) ............ (Login/Logout Command)
 Prune images ......................... (PruneCommand)
```

---

## Tally
- `[NOW]` 11 items across 4 menus (shipped).
- `[T1]` 6 proposed: SSH, Copy IP, Open VNC, Reveal in Finder, Show Logs, Take Screenshot.
- `[T2]` 6 proposed: VM Info, Exec shell, Force Stop, Restart, Clipboard, Screen Share.
- `[T3]` deferred to a Manager window.

## Recommendation
Build **[T1]** first (new **Connect** + **File** menus, **View ▸ Take Screenshot**); it's the best
value-to-effort, reuses `command` logic via `{in-proc}`/`{subproc}`, and stays idiomatic. **[T2]** as
fast-follow. **[T3]** only if you want a full GUI VM manager (separate, larger effort).

All new code lands in `internal/ui`; `internal/command` stays headless; idiomatic frameworks only.
