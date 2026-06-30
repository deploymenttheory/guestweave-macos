# Configuration

guestweave reads a settings file plus a few environment variables. Settings are
edited with `weave config …` or by hand.

## Settings file

Location: `~/.config/weave/config.yaml` (or `$XDG_CONFIG_HOME/weave/config.yaml`).
A missing file means all-defaults. YAML is canonical; the same keys work as JSON.

```yaml
# Default storage location: a name from storageLocations, or an absolute path.
defaultStorage: work
storageLocations:
  work: /Volumes/fast/weave
  scratch: ~/weave-scratch

# Override the OCI/IPSW cache directory (default: <home>/cache).
cacheDir: /Volumes/fast/weave-cache

# Default registry coordinates for short image references.
registry:
  host: ghcr.io
  organization: my-org

# Named registry profiles (preferred over the single 'registry' block).
registries:
  - name: ghcr
    host: ghcr.io
    organization: my-org
    default: true
  - name: lab
    host: registry.lab.internal
    insecure: true

# Enterprise clipboard policy applied to VMs without their own (see Clipboard).
defaultClipboardPolicy:
  enabled: true
  direction: bidirectional      # disabled | bidirectional | hostToGuest | guestToHost
  formats:
    text: true
    rich: true
    image: false
  fileTransfer: false

# File-logger size cap and rotation (see Logging).
logging:
  maxSizeMB: 10                  # cap per file before rotation; 0 = unlimited
  keepRotated: true             # keep one .old generation, or truncate when false
```

Equivalent JSON uses the same keys (e.g. `{"defaultStorage":"work","logging":{"maxSizeMB":10}}`).

## `weave config` commands

```
config get                                       show effective configuration
config storage list                              list named storage locations
config storage add <name> <path>                 add a named location
config storage remove <name>                     remove a location
config storage default <name>                    set the default storage
config cache dir [path]                          show or set the cache directory
config registry status                           show registry defaults
config registry ghcr [--registry H] [--organization O]
config network interfaces                        list bridgeable host interfaces
config logging [maxSizeMB [N] | keepRotated [true|false]]
```

```sh
weave config get
weave config storage add work /Volumes/fast/weave
weave config storage default work
weave config logging maxSizeMB 20
weave config logging keepRotated false
```

Changes are written to the settings file immediately. A running process reads
settings at startup, so changes apply to **new** invocations.

## Storage locations

`defaultStorage` chooses where VMs live (the "home" tree). It is resolved in this
order (see also `WEAVE_HOME`):

1. `WEAVE_HOME` environment variable, if set.
2. `defaultStorage` from settings (a `storageLocations` name or absolute path).
3. `~/.weave`.

## Clipboard

A single policy-driven engine syncs the host and guest pasteboards over the
embedded `weave-guestd` agent (deployed to the guest over SSH), for both Linux
and macOS guests. It is **on by default** (built-in policy: enabled,
bidirectional, all formats) and owns the clipboard — the SPICE agent clipboard is
not also wired, so there is one owner. `--no-clipboard`, or a resolved policy
whose direction is `disabled`, turns the clipboard off entirely.

Precedence: per-run `--clipboard*` flags → the VM's own policy →
`defaultClipboardPolicy` in settings → built-in default.

Per-run flags (see [CLI Reference → run](cli-reference.md#run)):
`--clipboard` / `--no-clipboard`, `--clipboard-direction`, `--clipboard-formats`,
`--clipboard-files`, `--clipboard-user`, `--clipboard-password`,
`--clipboard-session-mbps`, `--clipboard-bandwidth-pct`, `--clipboard-max-bytes`.

**Guest requirements.** The agent connects over SSH, so the guest must be
reachable with the configured `--clipboard-user` / `--clipboard-password`
(default `weave` / `weave`). A **Linux** guest also needs a clipboard CLI
(`xclip` for X11 or `wl-clipboard` for Wayland) and a display: a desktop session
(headed) or a headless `Xvfb` (e.g. `Xvfb :99 -ac`) — the agent discovers the
active Wayland socket or X display automatically. **macOS** guests use the system
pasteboard directly and need no extra tooling. The host binary must be built with
the agent embedded (`make build`); see the build notes in `CLAUDE.md`.

The run window's **Control ▸ Clipboard Status…** shows the resolved direction
(read-only; the policy is fixed at launch).

## Environment variables

| Variable | Effect |
|----------|--------|
| `WEAVE_HOME` | Override the home tree (VMs, cache, tmp, logs) |
| `XDG_CONFIG_HOME` | Override the settings-file directory (`…/weave/config.yaml`) |
| `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` | Honoured for registry/IPSW HTTP (Go-native; macOS system proxy is **not** consulted) |
| `CI` | Suppresses opening the VNC viewer automatically on `run --vnc` |

See also [Logging](logging.md) and [Networking](networking.md).
