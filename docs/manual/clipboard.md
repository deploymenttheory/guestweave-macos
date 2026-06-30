# Clipboard

guestweave includes an **enterprise clipboard**: a single, policy-driven engine
that mirrors the host and guest pasteboards — **text, rich text, images, and
files, in both directions**, for macOS and Linux guests. The policy is a control
plane: it governs *what* may cross the host⇄guest boundary, enforces those rules
on both sides, can be **changed live on a running VM**, and can emit an **audit
trail** of everything that crosses.

It is **on by default** (built-in policy: enabled, bidirectional, all formats and
file transfer, a 50 MiB per-item cap, no bandwidth limit) and owns the clipboard —
the SPICE-agent clipboard is not also wired, so there is exactly one owner.
`--no-clipboard`, or a resolved policy whose direction is `disabled`, turns it off
entirely.

## How it works

A resident `weave-guestd` agent runs inside the guest's GUI login session and
talks to the host over a dedicated **virtio serial channel** (not SSH, not vsock).
SSH is used only to install/upgrade that agent the first time and to resolve the
guest IP. On the host the engine reads and writes `NSPasteboard`; the guest backend
uses the system pasteboard on macOS, or `xclip` / `wl-clipboard` on Linux.

A **file copy is files-authoritative**: when the pasteboard holds files, only the
files are synced (the incidental file-name text and icon image a Finder copy also
advertises are dropped, so the round-trip isn't lossy and doesn't loop).

## Policy model

| Field | Meaning |
|-------|---------|
| `enabled` | Master switch. |
| `direction` | `disabled` · `bidirectional` · `hostToGuest` · `guestToHost`. |
| `formats` | Category toggles: `plainText`, `richText` (RTF/RTFD/HTML), `image` (PNG/TIFF/PDF). |
| `fileTransfer` | The file channel, independent of the format toggles. |
| `allowedTypes` | Fine-grained per-format allow-list (e.g. `text/html`); **authoritative when set**, overriding the category toggles. Does not govern the file channel. |
| `maxContentBytes` | Per-item/file size cap (default 50 MiB). |
| `sessionMbps` × `bandwidthPct` | Bandwidth budget: that percentage of the declared session bandwidth (both unset = unlimited). |
| `auditLog` | Emit a structured audit record per transfer and rejection. |

Canonical formats on the wire: `text/plain`, `text/rtf`, `text/html`,
`image/png`, `image/tiff`, `application/pdf`, plus the independent `files` channel.

### Enforcement

All rules are enforced on the host **and** mirrored to the guest:

- **Direction** is checked every sync cycle.
- **Formats / `allowedTypes`** filter host capture, and the allow-list is sent to
  the guest so it only ever returns permitted formats.
- **`maxContentBytes`** is applied on host capture *and* re-enforced on the host
  when receiving from the guest — an oversize guest item can never reach the host,
  even if the guest ignores the advertised cap.
- **Bandwidth** is throttled in both directions.

## Configuring the policy

There are four ways to set the policy, in increasing precedence:
**built-in default → settings default → per-VM config → per-run flags**, and then
**live updates** on top of a running engine.

### 1. Global default — `weave config clipboard`

Applies to any VM without its own policy. Stored in the settings file
(`~/.config/weave/config.yaml`, key `defaultClipboardPolicy`).

```sh
weave config clipboard                              # show the effective default
weave config clipboard --direction hostToGuest --formats text,rich \
    --files off --allowed-types text/html --audit on --max-bytes 1048576
weave config clipboard reset                        # back to the built-in default
```

### 2. Per-VM — `weave set <name> --clipboard-*`

Persists a policy onto one VM's `config.json` (layered over its existing policy or
the built-in default).

```sh
weave set my-vm --clipboard-direction guestToHost --clipboard-files off \
    --clipboard-allowed-types text/plain,text/html --clipboard-audit on
```

Flags: `--clipboard-enabled`, `--clipboard-direction`, `--clipboard-formats`,
`--clipboard-files`, `--clipboard-allowed-types`, `--clipboard-audit`,
`--clipboard-session-mbps`, `--clipboard-bandwidth-pct`, `--clipboard-max-bytes`.

### 3. Per-run — `weave run <name> --clipboard-*`

Overrides for a single launch (see [CLI Reference → run](cli-reference.md#run)):
`--clipboard` / `--no-clipboard`, `--clipboard-direction`, `--clipboard-formats`,
`--clipboard-files`, `--clipboard-allowed-types`, `--clipboard-audit`,
`--clipboard-session-mbps`, `--clipboard-bandwidth-pct`, `--clipboard-max-bytes`,
plus `--clipboard-user` / `--clipboard-password` (agent install credentials).

### 4. Live — `weave clipboard set <name>` (no restart)

Reshape the policy of an **already-running** VM. The change applies within one
sync cycle; add `--persist` to also write it to the VM's config.

```sh
weave clipboard get my-vm                           # show the running policy
weave clipboard set my-vm --direction guestToHost   # block host→guest, live
weave clipboard set my-vm --max-bytes 1048576 --audit on
weave clipboard set my-vm --direction bidirectional --persist
```

Flags mirror the policy fields: `--enabled`, `--direction`, `--formats`, `--files`,
`--allowed-types`, `--audit`, `--session-mbps`, `--bandwidth-pct`, `--max-bytes`,
`--persist`. Live updates adjust a clipboard that is already running; a VM started
with `--no-clipboard` (or a disabled policy) has no engine to update.

> `--clipboard-formats` / `--formats` take the CSV tokens `text,rich,image`. In the
> settings/VM-config files the same toggles are the YAML/JSON keys `plainText`,
> `richText`, `image`.

## Audit log

With `auditLog` on (via any of the `*-audit` flags) or the `WEAVE_CLIP_AUDIT=1`
environment variable, the engine writes one JSON record per applied transfer,
per policy rejection (e.g. oversize), and per live policy change to
`~/.weave/logs/weave.clipboard-audit.log` (also emitted to OpenTelemetry):

```json
{"time":"2026-06-30T19:39:23Z","vm":"my-vm","direction":"g2h","decision":"applied","formats":["text/plain"],"bytes":19}
{"time":"2026-06-30T19:40:01Z","vm":"my-vm","direction":"g2h","decision":"blocked","reason":"oversize","files":[{"name":"big.zip","size":83886080}],"bytes":83886080}
```

Set `WEAVE_CLIP_DEBUG=1` instead for verbose, human-readable per-cycle sync
tracing on stderr (development, not an audit source).

## HTTP API

A running VM's policy can be updated remotely (the engine must be running):

```
POST /weave/vms/{name}/clipboard
{ "direction": "guestToHost", "maxBytes": 1048576, "audit": "on", "persist": true }
```

Omitted fields are left unchanged. The launch API (`POST /weave/vms/{name}/run`)
accepts the same clipboard fields as the run flags. See the OpenAPI document at
`GET /weave/openapi.yaml`.

## Guest requirements

The agent installs over SSH, so the guest must be reachable with the configured
`--clipboard-user` / `--clipboard-password` (default `weave` / `weave`). A
**Linux** guest also needs a clipboard CLI (`xclip` for X11 or `wl-clipboard` for
Wayland) and a display — a desktop session (headed) or a headless `Xvfb` (e.g.
`Xvfb :99 -ac`); the agent finds the active Wayland socket or X display
automatically. **macOS** guests use the system pasteboard directly and need no
extra tooling. The host binary must be built with the agent embedded
(`make build`); a plain `go build` that skips the agent disables the clipboard.
See the build notes in `CLAUDE.md`.

## UI

The run window's **Control ▸ Clipboard Status…** shows the resolved direction and
live connection health. To change the policy of a running VM, use
`weave clipboard set` (above).

## See also

- [CLI Reference](cli-reference.md) — `run`, `set`, `config`, and `clipboard` flags
- [Configuration](configuration.md) — the settings file and precedence
- [Logging](logging.md) — log files, including the clipboard audit log
