# guestweave — build, code-sign, and install.
#
# Naming model:
#   - Product / app identity : guestweave (codesign identifier, os_log subsystem,
#                              OTel service name, the built artifact).
#   - CLI command you type    : weave (a symlink onto your PATH -> ./guestweave).
#
# Usage:
#   make build              build + code-sign ./guestweave
#   make install            build, then symlink `weave` into $(PREFIX)
#   make uninstall          remove the `weave` symlink
#   make clean              remove ./guestweave
#   make install PREFIX=/usr/local/bin   (system-wide; may need sudo)

BINARY       := guestweave
COMMAND      := weave
IDENTIFIER   := com.deploymenttheory.guestweave
ENTITLEMENTS := entitlements.plist
PREFIX       ?= $(HOME)/.local/bin
SPEC         := internal/httpapi/schema/openapi.yaml
SPEC_RULESET := internal/httpapi/schema/vacuum-ruleset.yaml
VACUUM       ?= vacuum

# Guest agent (weave-guestd) cross-compile: the host embeds one binary per guest
# OS/arch and deploys it on demand to drive the clipboard (and future modules).
# Without these binaries the host engine falls back to disabled clipboard, so the
# `build` target depends on `agent` to keep the embed dir populated.
AGENT_PKG    := ./internal/guestweaveagent/cmd/weave-guestd
AGENT_DIST   := internal/guestweaveagent/agentbin/dist
AGENT_TARGETS := darwin/arm64 linux/arm64 linux/amd64

.PHONY: all build agent sign install uninstall clean test-api lint-openapi acceptance-serve acceptance-api-vm acceptance-clipboard clip-lab-linux clip-lab-macos

all: build

## agent: cross-compile the weave-guestd guest agent into the embed dir, one
## binary per guest OS/arch. Pure-Go (CGO disabled), so it cross-compiles from
## any host. These artifacts are git-ignored and embedded by //go:embed dist.
agent:
	@mkdir -p $(AGENT_DIST)
	$(foreach t,$(AGENT_TARGETS), \
		echo "building weave-guestd-$(subst /,-,$(t))"; \
		CGO_ENABLED=0 GOOS=$(word 1,$(subst /, ,$(t))) GOARCH=$(word 2,$(subst /, ,$(t))) \
			go build -trimpath -ldflags="-s -w" \
			-o $(AGENT_DIST)/weave-guestd-$(subst /,-,$(t)) $(AGENT_PKG); \
	)

## build: cross-compile the guest agent, then compile and ad-hoc code-sign the
## guestweave binary at the repo root (the host embeds the agent binaries).
build: agent
	go build -o $(BINARY) .
	codesign --force --sign - --identifier $(IDENTIFIER) --entitlements $(ENTITLEMENTS) $(BINARY)

## sign: re-sign an already-built binary (rarely needed; build signs too).
sign:
	codesign --force --sign - --identifier $(IDENTIFIER) --entitlements $(ENTITLEMENTS) $(BINARY)

## install: build, then symlink `weave` -> ./guestweave into PREFIX (default ~/.local/bin).
## The symlink points at the repo binary, so future `make build`s take effect with no reinstall.
install: build
	@mkdir -p $(PREFIX)
	ln -sf $(abspath $(BINARY)) $(PREFIX)/$(COMMAND)
	@echo "Linked $(PREFIX)/$(COMMAND) -> $(abspath $(BINARY))"
	@case ":$$PATH:" in *":$(PREFIX):"*) : ;; \
		*) echo "warning: $(PREFIX) is not on your PATH — add it to use \`$(COMMAND)\`" ;; esac
	@echo "Done. Try: $(COMMAND) --help"

## uninstall: remove the `weave` symlink from PREFIX.
uninstall:
	rm -f $(PREFIX)/$(COMMAND)
	@echo "Removed $(PREFIX)/$(COMMAND)"

## clean: remove the built binary and the embedded guest-agent artifacts.
clean:
	rm -f $(BINARY)
	rm -f $(AGENT_DIST)/weave-guestd-*

## test-api: HTTP API unit tests — OpenAPI validity + router/spec drift (no VM).
test-api:
	go test ./internal/httpapi/...

## lint-openapi: lint the OpenAPI document with vacuum.
## Install once: go install github.com/daveshanley/vacuum@latest
lint-openapi:
	$(VACUUM) lint -d -r $(SPEC_RULESET) $(SPEC)

## acceptance-serve: contract + live OpenAPI-conformance suite (no VM).
acceptance-serve:
	go run ./internal/acceptance -suites serve

## acceptance-api-vm: full happy-path over HTTP against a Linux OCI guest.
## Requires the Virtualization framework and a cached image:
##   weave pull ghcr.io/cirruslabs/ubuntu:latest
## Set WEAVE_ACC_API_HEAVY=1 to also exercise the export/import round-trip.
acceptance-api-vm:
	go run ./internal/acceptance -suites api-vm

## acceptance-clipboard: real host ⇄ guest text round-trip through the unified
## weave-guestd clipboard engine, for a Linux and (optionally) a macOS guest.
## Depends on `agent` so the harness's binary embeds the guest agent.
##   Linux: needs the image cached — weave pull ghcr.io/cirruslabs/ubuntu:latest
##   macOS: set WEAVE_ACC_MACOS_GUEST to a provisioned, stopped macOS VM name
##          (creds WEAVE_ACC_MACOS_USER / WEAVE_ACC_MACOS_PASSWORD, default weave)
## Each guest's case skips cleanly when that guest is unavailable.
acceptance-clipboard: agent
	go run ./internal/acceptance -suites clipboard

## clip-lab-linux: stand up a reusable Linux clipboard lab VM headed, so you can
## validate the clipboard by eye (copy text host ⇄ guest). Builds + signs first.
## Override the base image with IMAGE=...; the VM is named weave-cliplab-linux.
IMAGE ?= ghcr.io/cirruslabs/ubuntu:latest
clip-lab-linux: build
	./$(BINARY) pull $(IMAGE)
	./$(BINARY) clone $(IMAGE) weave-cliplab-linux || true
	@echo "Booting weave-cliplab-linux headed with clipboard. In the guest, install a"
	@echo "desktop session (or xclip + an X server) to exchange text with the host."
	./$(BINARY) run weave-cliplab-linux --clipboard --clipboard-user admin --clipboard-password admin

## clip-lab-macos: boot a provisioned macOS VM headed with clipboard for by-eye
## validation. Set VM=<name> (a VM you created from an IPSW and set up).
clip-lab-macos: build
	@test -n "$(VM)" || { echo "set VM=<macos-vm-name>"; exit 1; }
	./$(BINARY) run $(VM) --clipboard
