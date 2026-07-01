# CLAUDE.md

Guidance for Claude Code when working in this repo (`guestweave-macos`, Go module
`github.com/deploymenttheory/guestweave`). darwin-only; requires macOS + the
Virtualization framework.

## Building & running

The CLI builds into a `guestweave` binary at the repo root. **VM operations require
the binary to be code-signed with the Virtualization entitlement** — otherwise they
fail with `VZErrorDomain:10004 "Unable to connect to installation service"`. After
*every* rebuild you must re-sign.

Prefer `make build`: it cross-compiles the guest agent into the embed dir, builds
the host binary, and code-signs it in one step:

```sh
make build
```

The manual equivalent (re-sign after *every* rebuild):

```sh
make agent                 # cross-compile weave-guestd into agentbin/dist (per guest OS/arch)
go build -o guestweave .
codesign --force --sign - --identifier com.deploymenttheory.guestweave --entitlements entitlements.plist guestweave
```

**Clipboard needs the agent embedded.** The host embeds one `weave-guestd` binary
per guest OS/arch (via `//go:embed dist`) and deploys it over SSH; a plain
`go build .` that skips `make agent` embeds none, so the clipboard engine disables
itself. Always build the agent (or just use `make build`) when testing clipboard.

The binary file and application identity (codesign identifier, os_log subsystem,
OTel service name) are `guestweave` / `com.deploymenttheory.guestweave`; only the
CLI command vocabulary (`weave <subcommand>` in usage/help) and the window menus
stay branded `weave`.

`./guestweave` is git-ignored. `entitlements.plist` is the ad-hoc dev entitlement
(`com.apple.security.virtualization` + `com.apple.security.hypervisor`);
`com.apple.vm.networking` needs a notarized Developer ID and is omitted.

Smoke test (long — full macOS install): `./guestweave create <name> --from-ipsw <ipsw>`
then `./guestweave run <name>` (headed) or `--no-graphics` (headless). A cached IPSW
usually lives under `~/.weave/cache/IPSWs/`.

## SDK relationship

The macOS framework bindings come from the published module
`github.com/deploymenttheory/go-bindings-macosplatform` (idiomatic layer at
`opinionated/idiomatic/framework/<fw>`). To pick up a new SDK feature, bump the
version: `go get github.com/deploymenttheory/go-bindings-macosplatform@vX.Y.Z`.
The SDK source repo is at `/Users/dafyddwatkins/GitHub/sdk/go-bindings-macosplatform`
(idiomatic packages are **codegen output** — regenerate with
`go run ./cmd/generate/ idiomatic` there, never hand-edit them).

## Working rules

- **gopls/IDE diagnostics are unreliable here** (stale cross-module phantoms,
  especially right after an SDK version bump). Ground truth is
  `go build ./internal/<pkg>/...`.
- An ongoing effort is migrating weave off the SDK's *raw* `bindings/frameworks/*`
  onto the idiomatic layer (bar: zero raw framework imports). Check progress with
  `grep -rl go-bindings-macosplatform/bindings/frameworks internal/ cmd/`.
- Idiomatic `With*` collection setters and variadic array params pass `nil` for the
  *empty* case, which the raw setter dereferences (SIGSEGV). Only call them with a
  non-empty slice; a fresh config defaults every device list to empty.
- The `internal/acceptance` package has a pre-existing darwin build-tag bug, so
  `go build ./...` fails there; build everything else with
  `go build $(go list ./... | grep -v /internal/acceptance)`.

## Verify

```sh
go build $(go list ./... | grep -v /internal/acceptance)
go vet ./internal/...
go test ./internal/...
```
