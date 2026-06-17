//go:build linux && arm64

package bwrap

import _ "embed"

//go:embed bin/bwrap_linux_arm64
var bwrapBinary []byte
