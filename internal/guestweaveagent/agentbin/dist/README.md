# weave-guestd binaries

This directory holds the cross-compiled guest agent binaries embedded into
weave by `internal/guestweaveagent/agentbin`. They are produced by `make agent`
(which `make build` runs automatically):

```
weave-guestd-darwin-arm64
weave-guestd-linux-arm64
weave-guestd-linux-amd64
```

This README is a committed placeholder so `//go:embed dist` always compiles even
before the binaries are built. When a target's binary is missing (e.g. a plain
`go build .` that skips the agent step), the host clipboard engine disables
itself. The binaries themselves are build artifacts and are not committed.
