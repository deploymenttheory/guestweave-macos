# Networking

guestweave models a VM's network as a **profile** — a named topology you can set
at `create` time (persisted) or at `run` time (overriding). The first NIC is the
primary; its MAC is bound from the VM config.

## Profiles

| Profile | Connectivity | Backend | Privilege |
|---------|--------------|---------|-----------|
| `nat` | internet ✓, host ✓, VM↔VM ✓, SSH ✓ | Apple shared/NAT | none |
| `internet-only` | internet ✓, host ✗, peers ✗ | softnet (blocks private ranges) | root |
| `isolated` | air-gapped — no egress at all | softnet (blocks `0.0.0.0/0`) | root |
| `vm-lab` | host segment ✓ + VM↔VM ✓, internet ✗ | vmnet host-mode | entitlement or root |
| `bridged` | guest is a LAN peer, internet ✓, SSH ✓ | bridged to a host interface | entitlement or root |

Notes:
- Under **softnet** (`internet-only`, `isolated`) the guest's default gateway is
  softnet's own userspace NAT, not the host. Manage these guests via **VNC** or
  the **vsock agent** (`exec`); SSH only works if a port is exposed.
- **vm-lab** is a vmnet host-mode segment for pentest/exploit labs — VMs on the
  segment interconnect and reach the host, but have no internet.
- **bridged** requires the `com.apple.vm.networking` entitlement (a notarized
  Developer ID). Root does **not** bypass it — the ad-hoc dev build cannot use
  bridged. See [Installation](installation.md).

## Setting a profile

At create time (persisted into the VM):
```sh
weave create my-vm --linux --net-profile nat
```

At run time (overrides the persisted profile):
```sh
sudo weave run my-vm --net-profile internet-only
sudo weave run my-vm --net-profile vm-lab
```

## Run-time network flags

| Flag | Description |
|------|-------------|
| `--net-profile <name>` | Use a named profile (`nat`/`internet-only`/`isolated`/`vm-lab`/`bridged`) |
| `--net-device <spec>` (repeatable) | Add an explicit NIC |
| `--net-bridged <iface>` (repeatable) | Bridge onto a host interface |
| `--net-host` | Host-only networking |
| `--net-softnet` | Enable softnet for the NIC |
| `--net-softnet-allow <cidrs>` | Softnet allow-list |
| `--net-softnet-block <cidrs>` | Softnet block-list |
| `--net-softnet-expose <h:g>` | Forward a host port to the guest (e.g. `2222:22` to keep SSH reachable under softnet) |

List host interfaces eligible for bridging:
```sh
weave config network interfaces
```

## Reaching a guest

- `nat`, `vm-lab`, `bridged`: `guestweave ssh <name>` / `guestweave ip <name>`
  (use `--resolver arp` for bridged guests whose lease is on the LAN, not weave).
- `internet-only`, `isolated`: use `guestweave exec` (vsock agent) or VNC; expose
  a port with `--net-softnet-expose 2222:22` if you need SSH.

See also [CLI Reference → run](cli-reference.md#run) and [Configuration](configuration.md).
