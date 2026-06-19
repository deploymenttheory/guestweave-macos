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

.PHONY: all build sign install uninstall clean

all: build

## build: compile and ad-hoc code-sign the guestweave binary at the repo root.
build:
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

## clean: remove the built binary.
clean:
	rm -f $(BINARY)
