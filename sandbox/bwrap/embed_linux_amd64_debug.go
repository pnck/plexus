//go:build linux && amd64 && debug

package bwrap

import _ "embed"

// Debug builds (-tags debug) embed a debug-built bwrap (meson --buildtype=debug:
// -O0 -g, assertions on, not stripped) so the sandbox helper is debuggable end to
// end. Release builds embed the optimized bwrap from embed_linux_amd64.go instead.
//
//go:embed bin/bwrap_linux_amd64_debug
var bwrapBinary []byte
