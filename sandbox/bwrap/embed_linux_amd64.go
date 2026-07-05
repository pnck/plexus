//go:build linux && amd64 && !debug

package bwrap

import _ "embed"

//go:embed bin/bwrap_linux_amd64
var bwrapBinary []byte
