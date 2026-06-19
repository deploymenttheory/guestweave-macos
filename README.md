# guestweave-macos

This repository is home to guestweave for macos. This project's purpose is to provide a configurable cli tool for the lifecycle management of virtual compute on macOS hosts. It's written in pure golang and consumes the
programatically generated idiomatic macosframeworks from the [go-bindings-macosplatform](https://github.com/deploymenttheory/go-bindings-macosplatform) project as it's foundation.

The motivation for this project stems from the following factors:
- a worthwile project that proves the value of the go-bindings-macosplatform initative
- provides a means for the provisioning of ephmerial compute on macOS for various enterprise usecases such as; application packaging, running of mcp-servers and other ai tools in a guardrailed environment, testing of device builds, macOS runners for pipeline execution and

## Building

The CLI builds with the Go toolchain into a `guestweave` binary at the repo
root. **The binary must be code-signed with the Virtualization entitlement** —
without it, any VM operation fails with `VZErrorDomain:10004 "Unable to connect
to installation service"` (or similar). Ad-hoc signing is sufficient for local
dev:

```sh
# from the repo root
go build -o guestweave .
codesign --force --sign - --identifier com.deploymenttheory.guestweave --entitlements entitlements.plist guestweave
```

The binary file and the application identity (codesign identifier, os_log
subsystem, OpenTelemetry service name) are `guestweave` /
`com.deploymenttheory.guestweave`; only the CLI's own command vocabulary stays
branded `weave` (`weave <subcommand>` in usage/help and the window menus).

`go build` darwin-only; requires macOS with the Virtualization framework. The
`guestweave` binary is git-ignored. `entitlements.plist` grants
`com.apple.security.virtualization` + `com.apple.security.hypervisor`;
`com.apple.vm.networking` (bridged networking) needs a notarized Developer ID
and is omitted from the ad-hoc dev entitlements.

## Running

```sh
# create a macOS VM from a restore image, then boot it with a UI window
./guestweave create my-vm --from-ipsw /path/to/restore.ipsw --disk-size 50
./guestweave run my-vm

# other lifecycle verbs
./guestweave list
./guestweave run my-vm --no-graphics   # headless
./guestweave stop my-vm
```

Run `./guestweave --help` for the full subcommand list (the in-tool usage still
refers to the commands as `weave <subcommand>`). 