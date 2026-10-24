//go:build !cgo

package collector

// The darwin collector is the cgo shim, so without cgo there is nothing to
// build. Fail here, naming the fix, rather than with "undefined: collector.New"
// in main. Cross-compiling for darwin turns cgo off by default.
var _ = darwin_needs_cgo_build_on_a_Mac_with_CGO_ENABLED_1
