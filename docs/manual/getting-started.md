# Getting Started

This walks through creating and running your first VM, in both CLI and UI modes.
First build and **code-sign** the binary — see [Installation](installation.md).

## 1. Get a VM

You can create a VM from a macOS restore image (IPSW), create an empty Linux VM,
or pull a prebuilt image from a registry.

### From a macOS IPSW

```sh
# 'latest' downloads the newest supported restore image; or pass a path/URL
weave create my-mac --from-ipsw latest --disk-size 80
weave create my-mac --from-ipsw ~/Downloads/UniversalMac.ipsw
```

macOS install runs unattended through to the login window (it takes a while).
`weave ipsw` prints the latest supported IPSW URL without creating a VM.

### A Linux VM

```sh
weave create my-linux --linux --disk-size 40
```

### Pull a prebuilt image

```sh
weave pull ghcr.io/cirruslabs/ubuntu:latest
weave clone ghcr.io/cirruslabs/ubuntu:latest my-linux
```

`clone` makes a local, runnable copy from a pulled (or remote) image.

## 2. Run it

### UI mode (graphical window)

```sh
weave run my-mac
```

A native window opens showing the guest's screen, with a menu bar for guest
access (SSH, Copy IP), power control, screenshots, and full-screen. See the
[UI Guide](ui-guide.md).

### Headless

```sh
weave run my-linux --no-graphics      # no window
weave run my-linux --vnc              # serve a VNC endpoint instead
```

## 3. Reach the guest

```sh
weave ip my-linux                     # resolve the guest IP
weave ssh my-linux                    # interactive SSH (default user/pass: weave/weave)
weave exec -it my-linux /bin/sh       # run a command via the vsock agent (macOS 14+)
```

## 4. Manage its lifecycle

```sh
weave list                            # all VMs and their state
weave get my-mac --format json        # one VM's config as JSON
weave stop my-mac                      # graceful stop
weave suspend my-mac                    # save state (macOS 14+)
weave delete my-old-vm
```

## Next steps

- Full command surface: [CLI Reference](cli-reference.md)
- Driving the window: [UI Guide](ui-guide.md)
- Network modes: [Networking](networking.md)
- Tuning settings: [Configuration](configuration.md)
