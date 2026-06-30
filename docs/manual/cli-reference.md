# CLI Reference

Every guestweave subcommand. Invoke as `weave <command> [flags] [args]`
(the in-tool usage prints them as `weave <command>`). Run `weave --help`
for the live list.

Conventions: `<name>` is a local VM name; `<remote-name>` is a registry
reference like `ghcr.io/org/image:tag`. Flags must precede positional arguments
unless noted.

- [Lifecycle](#lifecycle): create, run, stop, suspend, delete, rename, clone, list, get, set, fqn
- [Images & registry](#images--registry): pull, push, images, login, logout, import, export, prune, ipsw
- [Guest access](#guest-access): ssh, exec, ip
- [Diagnostics & config](#diagnostics--config): logs, config
- [Automation](#automation): serve, setup, version

---

## Lifecycle

### create
Create a new VM. Requires either `--from-ipsw` (macOS) or `--linux`.

| Flag | Default | Description |
|------|---------|-------------|
| `--from-ipsw <latest\|path\|url>` | — | Install macOS from a restore image (`latest` downloads the newest supported) |
| `--linux` | false | Create an empty Linux VM (boot media attached at run time via `--disk`) |
| `--disk-size <GB>` | 50 | Root disk size in GB |
| `--disk-format <format>` | raw | Disk image format (see `create --help` for supported formats) |
| `--net-profile <profile>` | — | Persist a default [network profile](networking.md) into the VM |

```sh
weave create my-mac --from-ipsw latest --disk-size 80
weave create my-linux --linux --net-profile nat
```

### run
Boot a VM. With no `--no-graphics`/`--vnc` it opens the [UI window](ui-guide.md).
This command owns the foreground process until the VM exits.

Display / window:

| Flag | Description |
|------|-------------|
| `--no-graphics` | Headless; no window |
| `--graphics` | Force a graphical device even for guests that default off |
| `--vnc` / `--vnc-experimental` | Serve a VNC endpoint; `--vnc-password <pw>` sets the password |
| `--show-screen` | View-only browser stream of the screen (implies headless + experimental VNC) |
| `--capture-system-keys` | Send system key combos (e.g. Cmd-Tab) to the guest |
| `--no-trackpad` / `--no-pointer` / `--no-keyboard` | Omit those input devices |
| `--no-audio` | Disable audio |

Power / state:

| Flag | Description |
|------|-------------|
| `--recovery` | Boot a macOS guest into recovery mode |
| `--suspendable` | Allow Suspend (save state) on macOS 14+ |
| `--nested` | Enable nested virtualization (where supported) |
| `--rosetta <tag>` | Expose Rosetta to a Linux guest under a mount tag |

Storage / sharing:

| Flag | Description |
|------|-------------|
| `--disk <spec>` (repeatable) | Attach a disk/ISO; append `:ro` for read-only |
| `--mount <iso>` (repeatable) | Sugar for `--disk <iso>:ro` |
| `--dir <name:path[:ro]>` (repeatable) | Share a host directory (VirtioFS) |
| `--shared-dir <spec>` (repeatable) | lume-style shared directory |
| `--usb-storage <path>` (repeatable) | Attach a USB mass-storage image |
| `--root-disk-opts <opts>` | Root disk options (e.g. sync mode) |

Networking — see [Networking](networking.md):
`--net-profile`, `--net-device` (repeatable), `--net-bridged` (repeatable),
`--net-softnet`, `--net-softnet-allow`, `--net-softnet-block`,
`--net-softnet-expose`, `--net-host`.

Clipboard (enterprise clipboard engine — see [Clipboard](clipboard.md)):
`--clipboard` / `--no-clipboard`, `--clipboard-user`, `--clipboard-password`,
`--clipboard-direction`, `--clipboard-formats`, `--clipboard-files`,
`--clipboard-allowed-types`, `--clipboard-audit`, `--clipboard-session-mbps`,
`--clipboard-bandwidth-pct`, `--clipboard-max-bytes`.

Serial console: `--serial` (PTY), `--serial-path <path>`.

```sh
weave run my-mac
weave run my-linux --no-graphics --net-profile isolated
weave run my-linux --dir share:/Users/me/shared:ro --vnc
```

### stop
Gracefully stop a running VM (powers it off).

| Flag | Default | Description |
|------|---------|-------------|
| `--timeout`, `-t <secs>` | 30 | Seconds to wait for graceful termination |

```sh
weave stop my-vm -t 60
```

### suspend
Save a running VM's state and stop it (macOS 14+; the VM must have been run with
`--suspendable`). Resumed automatically on the next `run`.

```sh
weave suspend my-vm
```

### delete
Delete one or more VMs.

```sh
weave delete old-vm another-vm
```

### rename
Rename a VM.

```sh
weave rename my-vm my-vm-renamed
```

### clone
Make a local, runnable copy from a source VM or a (local/remote) image.

| Flag | Default | Description |
|------|---------|-------------|
| `--registry <profile>` | — | Named registry profile to pull from |
| `--insecure` | false | Allow plain-HTTP / unverified registry |
| `--concurrency <n>` | 4 | Parallel layer downloads |
| `--deduplicate` | false | Deduplicate disk blocks on clone |
| `--prune-limit <n>` | 100 | Cache-prune limit during clone |

```sh
weave clone ghcr.io/cirruslabs/ubuntu:latest my-linux
```

### list
List VMs and their state.

| Flag | Default | Description |
|------|---------|-------------|
| `--source <local\|oci\|...>` | all | Filter by source |
| `--format <text\|json>` | text | Output format |
| `--quiet`, `-q` | false | Names only |

### get
Show one VM's configuration.

| Flag | Default | Description |
|------|---------|-------------|
| `--format <text\|json>` | text | Output format |

```sh
weave get my-vm --format json
```

### set
Modify a stopped VM's configuration.

| Flag | Description |
|------|-------------|
| `--cpu <n>` | CPU core count |
| `--memory <bytes>` | Memory size in bytes |
| `--display <WxH[:dpi]>` | Display geometry |
| `--display-refit` / `--no-display-refit` | Auto display reconfiguration on/off |
| `--disk <spec>` / `--disk-size <GB>` | Resize/replace the disk |
| `--random-mac` / `--random-serial` | Regenerate MAC / serial |
| `--clipboard-*` | Persist a clipboard policy onto this VM — see [Clipboard](clipboard.md) |

```sh
weave set my-vm --cpu 4 --memory 8589934592 --display 1920x1080
weave set my-vm --clipboard-direction guestToHost --clipboard-files off
```

### fqn
Print a VM's fully-qualified name (registry reference).

```sh
weave fqn my-vm
```

---

## Images & registry

### pull
Download an image from a registry into the local OCI cache.

| Flag | Default | Description |
|------|---------|-------------|
| `--registry <profile>` | — | Named registry profile |
| `--insecure` | false | Plain-HTTP / unverified |
| `--concurrency <n>` | 4 | Parallel layer downloads |
| `--deduplicate` | false | Deduplicate disk blocks |

```sh
weave pull ghcr.io/cirruslabs/ubuntu:latest
```

### push
Upload a local VM to one or more registry references.

| Flag | Default | Description |
|------|---------|-------------|
| `--registry <profile>` | — | Named registry profile |
| `--insecure` | false | Plain-HTTP / unverified |
| `--concurrency <n>` | 4 | Parallel layer uploads |
| `--chunk-size <bytes>` | 0 | Upload chunk size (0 = default) |
| `--label <k=v>` (repeatable) | — | Attach OCI labels |
| `--populate-cache` | false | Also populate the local cache from the push |

```sh
weave push my-vm ghcr.io/org/my-vm:latest
```

### images
List images available in a remote repository.

| Flag | Description |
|------|-------------|
| `--registry <profile>` | Named registry profile |
| `--insecure` | Plain-HTTP / unverified |
| `--quiet`, `-q` | Names only |

```sh
weave images ghcr.io/cirruslabs/ubuntu
```

### login / logout
Store / remove registry credentials (in the system keychain).

| `login` flag | Description |
|------|-------------|
| `--username <user>` | Username |
| `--password-stdin` | Read the password from stdin |
| `--insecure` | Plain-HTTP / unverified |
| `--no-validate` | Skip credential validation |

```sh
echo "$TOKEN" | weave login ghcr.io --username me --password-stdin
weave logout ghcr.io
```

### import / export
Move a VM to/from a local archive file.

```sh
weave export my-vm ./my-vm.tvm     # path optional (defaults near cwd)
weave import ./my-vm.tvm restored-vm
```

### prune
Reclaim space from the caches.

| Flag | Default | Description |
|------|---------|-------------|
| `--entries <caches>` | caches | What to prune |
| `--older-than <days>` | — | Only entries older than N days |
| `--space-budget <GB>` | — | Prune down to this cache budget |
| `--cache-budget <GB>` | — | **Deprecated** — use `--space-budget` |
| `--gc` | false | Garbage-collect stale temp dirs |

```sh
weave prune --older-than 30 --space-budget 50
```

### ipsw
Print the download URL of the latest supported macOS restore image.

```sh
weave ipsw
```

---

## Guest access

### ssh
Open an interactive SSH session, or run a remote command.

| Flag | Default | Description |
|------|---------|-------------|
| `--user`, `-u <user>` | weave | SSH user |
| `--password`, `-p <pw>` | weave | SSH password |
| `--timeout`, `-t <secs>` | 60 | Connection timeout |
| `--wait <secs>` | 0 | Wait up to N seconds for IP resolution |
| `--resolver <dhcp\|arp\|agent>` | dhcp | IP resolution strategy |

```sh
weave ssh my-vm
weave ssh my-vm uname -a
```

### exec
Run a command in a running VM over the vsock guest agent (macOS 14+; the guest
must have the agent installed). Flags precede the name; everything after the name
is the remote command.

| Flag | Description |
|------|-------------|
| `-i` | Interactive (attach stdin) |
| `-t` | Allocate a TTY |
| `-it` | Both |

```sh
weave exec -it my-vm /bin/sh
weave exec my-vm cat /etc/os-release
```

### ip
Resolve and print a running VM's IP address.

| Flag | Default | Description |
|------|---------|-------------|
| `--wait <secs>` | 0 | Wait up to N seconds for resolution |
| `--resolver <dhcp\|arp\|agent>` | dhcp | Resolution strategy |

```sh
weave ip my-vm --wait 30
```

### clipboard
Inspect or **live-update** a running VM's clipboard policy (no restart). See
[Clipboard](clipboard.md).

| Form | Description |
|------|-------------|
| `clipboard get <name>` | Print the running effective policy |
| `clipboard set <name> [flags]` | Apply policy changes live |

`set` flags (omitted = unchanged): `--enabled`, `--direction`, `--formats`,
`--files`, `--allowed-types`, `--audit`, `--session-mbps`, `--bandwidth-pct`,
`--max-bytes`, and `--persist` (also write the VM config).

```sh
weave clipboard get my-vm
weave clipboard set my-vm --direction guestToHost
weave clipboard set my-vm --max-bytes 1048576 --audit on --persist
```

---

## Diagnostics & config

### logs
View or clear the file logs (`weave.info.log` / `weave.error.log`). See
[Logging](logging.md).

| Form | Description |
|------|-------------|
| `logs <info\|error\|all>` | Print a log stream |
| `logs ... --lines <N>` | Tail the last N lines (0 = whole file) |
| `logs ... -f` / `--follow` | Follow (tail -f) |
| `logs clear` | Delete all log files (info, error, and rotated `.old`) |

```sh
weave logs error --lines 50
weave logs all -f
weave logs clear
```

### config
Get or set persisted settings. See [Configuration](configuration.md).

```
config get
config storage <list|add <name> <path>|remove <name>|default <name>>
config cache dir [path]
config registry <status|ghcr [--registry H] [--organization O]>
config network interfaces
config logging [maxSizeMB [N] | keepRotated [true|false]]
config clipboard [--direction ... --formats ... --files ... | reset]
```

```sh
weave config get
weave config logging maxSizeMB 20
weave config clipboard --direction hostToGuest --audit on
```

---

## Automation

### serve
Run the REST/HTTP API server (a VM-lifecycle API under `/weave/*`).

| Flag | Default | Description |
|------|---------|-------------|
| `--port <n>` | 7777 | Listen port (127.0.0.1) |
| `--mcp` | false | Expose the MCP (Model Context Protocol) interface |

```sh
weave serve --port 7777
weave serve --mcp
```

### setup
Run unattended macOS Setup Assistant (preset) or an AI-driven setup (agent).

| Flag | Default | Description |
|------|---------|-------------|
| `--mode <preset\|agent>` | preset | Setup strategy |
| `--unattended <preset-or-path>` | — | Preset name or config path |
| `--show-screen` | false | Stream the screen while setting up |
| `--model <id>` | claude-sonnet-4-6 | Model for agent mode |
| `--anthropic-key <key>` | — | API key for agent mode |
| `--max-iterations <n>` | 200 | Agent step cap |
| `--system-prompt <text>` | — | Override the agent system prompt |
| `--debug` / `--debug-dir <dir>` | — | Capture debug artifacts |

```sh
weave setup my-mac --unattended my-preset
```

### version
Print the build version.

```sh
weave version        # or: weave --version
```
