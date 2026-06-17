//go:build linux && amd64

package bwrap

import _ "embed"

//go:embed bin/bwrap_linux_amd64
var bwrapBinary []byte
