# Installation

## Requirements

- **macOS on Apple Silicon** with the Virtualization framework. macOS guests
  require an arm64 host; Linux guests run on Apple Silicon.
- **Go toolchain** to build from source (guestweave is darwin-only).
- Some features have extra requirements:
  - `suspend` and the guest agent (`exec`) need **macOS 14 (Sonoma) or newer**.
  - **softnet/vmnet** network profiles need **root** (`sudo`).
  - **bridged** networking needs the `com.apple.vm.networking` entitlement
    (a notarized Developer ID — not granted by the ad-hoc dev signing below).

## Naming: `guestweave` the app vs `weave` the command

These are deliberately different:

- **`guestweave`** is the product / app identity — the built binary file, the
  codesign identifier, the os_log subsystem, and the OpenTelemetry service name
  (`com.deploymenttheory.guestweave`).
- **`weave`** is the command you type. `make install` puts a `weave` symlink on
  your `PATH` pointing at the built `guestweave` binary, so day-to-day you run
  `weave <subcommand>` — which is also what the in-tool help and window menus say.

Before installing, you can always run the local build directly as
`./guestweave <subcommand>` from the repo root.

## Build (Makefile)

```sh
make build      # compile + ad-hoc code-sign -> ./guestweave
make install    # build, then symlink `weave` into ~/.local/bin
```

`make install` accepts `PREFIX=` to choose where the `weave` symlink goes
(default `~/.local/bin`; use `PREFIX=/usr/local/bin` for system-wide, which may
need `sudo`). The symlink points at the repo binary, so later `make build`s take
effect with no reinstall. `make uninstall` removes it.

Ensure the install directory is on your `PATH`:

```sh
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc   # if not already
```

## Build manually (no Makefile)

The Makefile just wraps these steps:

```sh
go build -o guestweave .
codesign --force --sign - \
  --identifier com.deploymenttheory.guestweave \
  --entitlements entitlements.plist \
  guestweave
# optional: make `weave` available on PATH
ln -sf "$(pwd)/guestweave" ~/.local/bin/weave
```

## Code-signing (required for VM operations)

**The binary must be code-signed with the Virtualization entitlement** (the build
step above does this). Without it, any VM operation fails with `VZErrorDomain:10004
"Unable to connect to installation service"` (or similar). Re-sign after **every**
rebuild — `make build` always does.

- `entitlements.plist` grants `com.apple.security.virtualization` +
  `com.apple.security.hypervisor`.
- `com.apple.vm.networking` (bridged networking) is intentionally **omitted** —
  it requires a notarized Developer ID. See [Networking](networking.md).

The application identity (codesign identifier, os_log subsystem, OpenTelemetry
service name) is `com.deploymenttheory.guestweave`. Only the CLI's command
vocabulary stays branded `weave` (`weave <subcommand>` in usage/help and the
window menus).

## Where guestweave keeps its files

| Path | Purpose | Override |
|------|---------|----------|
| `~/.weave` | home tree: VMs, cache, tmp, logs | `GUESTWEAVE_STORAGE_HOME` env var, or a default storage location in settings |
| `~/.weave/cache` | OCI image + IPSW cache | `cacheDir` in settings |
| `~/.weave/logs` | `weave.info.log` / `weave.error.log` | follows `GUESTWEAVE_STORAGE_HOME` |
| `~/.config/weave/config.yaml` | settings file | `XDG_CONFIG_HOME` |

See [Configuration](configuration.md) for the settings schema and environment
variables, and [Logging](logging.md) for the log files.

## Verify the install

After `make install` (use `./guestweave` instead if you only built):

```sh
weave version      # prints the build version
weave --help       # lists all subcommands
weave list         # lists VMs (empty on a fresh install)
```
