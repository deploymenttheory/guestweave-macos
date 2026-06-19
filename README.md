# guestweave-macos

This repository is home to guestweave for macos. This project's purpose is to provide a configurable cli tool for the lifecycle management of virtual compute on macOS hosts. It's written in pure golang and consumes the
programatically generated idiomatic macosframeworks from the [go-bindings-macosplatform](https://github.com/deploymenttheory/go-bindings-macosplatform) project as it's foundation.

The motivation for this project stems from the following factors:
- a worthwile project that proves the value of the go-bindings-macosplatform initative
- provides a means for the provisioning of ephmerial compute on macOS for various enterprise usecases such as; application packaging, running of mcp-servers and other ai tools in a guardrailed environment, testing of device builds, macOS runners for pipeline execution and

## Documentation

The full user manual lives in [`docs/manual`](docs/manual/README.md) and covers
both CLI and UI usage:

- [Installation](docs/manual/installation.md) — requirements, building, code-signing, file locations
- [Getting Started](docs/manual/getting-started.md) — create and run your first VM
- [CLI Reference](docs/manual/cli-reference.md) — every subcommand and flag
- [UI Guide](docs/manual/ui-guide.md) — the run window and its menus
- [Configuration](docs/manual/configuration.md) — settings file, `weave config`, environment variables
- [Networking](docs/manual/networking.md) — network profiles
- [Logging](docs/manual/logging.md) — viewing, capping, and clearing logs
- [Troubleshooting](docs/manual/troubleshooting.md) — common errors and fixes

## Building

The CLI builds with the Go toolchain into a `guestweave` binary at the repo
root. **The binary must be code-signed with the Virtualization entitlement** —
without it, any VM operation fails with `VZErrorDomain:10004 "Unable to connect
to installation service"` (or similar). Use the Makefile:

```sh
make build      # compile + ad-hoc code-sign -> ./guestweave
make install    # build, then symlink `weave` onto your PATH (~/.local/bin)
```

`make install` lets you type **`weave <subcommand>`** from anywhere; override the
location with `PREFIX=` (e.g. `make install PREFIX=/usr/local/bin`). Equivalent
manual steps:

```sh
go build -o guestweave .
codesign --force --sign - --identifier com.deploymenttheory.guestweave --entitlements entitlements.plist guestweave
ln -sf "$(pwd)/guestweave" ~/.local/bin/weave   # optional: the `weave` command
```

**Naming:** `guestweave` is the app/product identity (the built binary, codesign
identifier, os_log subsystem, OpenTelemetry service name —
`com.deploymenttheory.guestweave`); **`weave`** is the command you type (and what
the in-tool usage/help and window menus say). Re-sign after every rebuild
(`make build` always does).

`go build` darwin-only; requires macOS with the Virtualization framework. The
`guestweave` binary is git-ignored. `entitlements.plist` grants
`com.apple.security.virtualization` + `com.apple.security.hypervisor`;
`com.apple.vm.networking` (bridged networking) needs a notarized Developer ID
and is omitted from the ad-hoc dev entitlements.

## Running

```sh
# create a macOS VM from a restore image, then boot it with a UI window
weave create my-vm --from-ipsw /path/to/restore.ipsw --disk-size 50
weave run my-vm

# other lifecycle verbs
weave list
weave run my-vm --no-graphics   # headless
weave stop my-vm
```

(Before `make install`, run the local build as `./guestweave <subcommand>`.)

Run `weave --help` for the full subcommand list. For full details see the
[CLI Reference](docs/manual/cli-reference.md), [UI Guide](docs/manual/ui-guide.md),
and the rest of the [manual](docs/manual/README.md).
