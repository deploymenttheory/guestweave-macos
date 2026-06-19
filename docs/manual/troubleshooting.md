# Troubleshooting

## `VZErrorDomain:10004 — Unable to connect to installation service`

The binary is **not code-signed** with the Virtualization entitlement (or you
rebuilt without re-signing). Re-sign:

```sh
codesign --force --sign - \
  --identifier com.deploymenttheory.guestweave \
  --entitlements entitlements.plist \
  guestweave
```

Re-sign after every `go build`. See [Installation](installation.md).

## Bridged networking fails / process is killed

`bridged` (and `vm-lab`) need the `com.apple.vm.networking` entitlement, which
requires a notarized Developer ID — root does **not** bypass it, and an ad-hoc
dev build cannot use bridged. Use `nat` for internet + host access without
special privileges. See [Networking](networking.md).

## "There is already a running VM with the same MAC address"

Two VMs share a MAC. guestweave regenerates the MAC automatically on the next
run; or set a new one explicitly:

```sh
weave set my-vm --random-mac
```

## VM won't start — virtual machine limit exceeded

macOS limits the number of concurrently running VMs. Stop another VM first:

```sh
weave list                 # find running VMs
weave stop other-vm
```

## `weave exec` doesn't work

`exec` uses the vsock **guest agent** and requires **macOS 14+** on the host and
the agent installed in the guest. If the guest has no agent, use `ssh` instead
(when the network profile allows host→guest reachability).

## Can't resolve the guest IP / SSH

- `internet-only` and `isolated` profiles don't route host→guest by default —
  use `exec` (vsock) or VNC, or expose a port with
  `--net-softnet-expose 2222:22`.
- For a `bridged` guest, the DHCP lease is on the LAN, not weave — resolve via
  ARP: `weave ip my-vm --resolver arp` (or `ssh --resolver arp`).
- Give a freshly-booted guest time: `weave ip my-vm --wait 30`.

## Logs are full of old/unrelated errors

The log files are append-only across sessions. Old entries persist until
rotated or cleared — they aren't necessarily from your current run. Clear them:

```sh
weave logs clear           # or File ▸ Clear Logs… in the UI
```

See [Logging](logging.md).

## Registry pulls fail behind a proxy

guestweave honours `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` (Go-native) but does
**not** consult the macOS system proxy. Export the proxy env vars in your shell.

## A rebuild "doesn't take effect"

The `guestweave` binary is git-ignored and must be rebuilt **and re-signed**
after changes; a running `guestweave run` also keeps its old binary until you
stop and relaunch it.
