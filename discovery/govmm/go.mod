module govmm

go 1.26.2

require github.com/deploymenttheory/go-bindings-macosplatform v0.0.0

require github.com/ebitengine/purego v0.10.1 // indirect

// Use the local SDK clone with the patched idiomatic hypervisor bindings
// (HvVmConfigCreate/HvVcpuCreate/HvVmMap/… now emitted) instead of the pinned
// published v0.10.1, which predates the generator patch.
replace github.com/deploymenttheory/go-bindings-macosplatform => /Users/dafyddwatkins/GitHub/sdk/go-bindings-macosplatform
