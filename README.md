# guestweave-macos

This repository is home to guestweave for macos. This project's purpose is to provide a configurable cli tool for the lifecycle management of virtual compute on macOS hosts. It's written in pure golang and consumes the
programatically generated idiomatic macosframeworks from the [go-bindings-macosplatform](https://github.com/deploymenttheory/go-bindings-macosplatform) project as it's foundation.

The motivation for this project stems from the following factors:
- a worthwile project that proves the value of the go-bindings-macosplatform initative
- provides a means for the provisioning of ephmerial compute on macOS for various enterprise usecases such as; application packaging, running of mcp-servers and other ai tools in a guardrailed environment, testing of device builds, macOS runners for pipeline execution and 