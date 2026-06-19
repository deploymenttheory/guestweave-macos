# guestweave manual

guestweave is a CLI tool for lifecycle management of virtual machines on macOS
hosts, built on Apple's Virtualization framework via the
[go-bindings-macosplatform](https://github.com/deploymenttheory/go-bindings-macosplatform)
idiomatic bindings. It creates, runs, and manages macOS and Linux guests, pulls
and pushes VM images to OCI registries, and exposes both a command-line
interface and a graphical run window.

> **Naming:** `guestweave` is the app/product (the built binary and the codesign
> /os_log/OTel identity); **`weave`** is the recconemend as the command you type. `make install`
> puts a `weave` symlink on your `PATH`. Examples in this manual use `weave`;
> before installing you can run the local build as `./guestweave` from the repo.
> See [Installation](installation.md).

## Two ways to use guestweave

- **CLI mode** — every operation is a subcommand (`guestweave create`, `run`,
  `pull`, `ssh`, …). Scriptable and headless-friendly. See the
  [CLI Reference](cli-reference.md).
- **UI mode** — `guestweave run <name>` (without `--no-graphics`) opens a native
  AppKit window showing the guest's screen, with menus for guest access,
  screenshots, power control, and more. See the [UI Guide](ui-guide.md).

## Contents

| Page | What it covers |
|------|----------------|
| [Installation](installation.md) | Requirements, building, code-signing, where files live |
| [Getting Started](getting-started.md) | Create and run your first VM (CLI + UI) |
| [CLI Reference](cli-reference.md) | Every subcommand, its flags, and examples |
| [UI Guide](ui-guide.md) | The run window and every menu action |
| [Configuration](configuration.md) | Settings file, `weave config`, environment variables |
| [Networking](networking.md) | Network profiles (nat / internet-only / isolated / vm-lab / bridged) |
| [Logging](logging.md) | Viewing, capping, rotating, and clearing logs |
| [Troubleshooting](troubleshooting.md) | Common errors and fixes |

## Quick example

```sh
make install                     # build, sign, and put `weave` on your PATH
weave create my-vm --from-ipsw /path/to/restore.ipsw --disk-size 50
weave run my-vm                  # opens the UI window
weave ssh my-vm                  # or connect over SSH
weave stop my-vm
```

Requires macOS on Apple Silicon with the Virtualization framework. VM operations
need a code-signed binary — see [Installation](installation.md).
