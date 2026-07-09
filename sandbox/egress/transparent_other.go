//go:build !linux

package egress

import (
	"fmt"
	"net"
)

// The transparent (TPROXY / IP_TRANSPARENT) primitives are Linux-only. These stubs
// exist so the package builds everywhere; the proxy is only ever started inside a
// Linux netns.
var errLinuxOnly = fmt.Errorf("egress: transparent proxy is supported only on linux")

func readUDPOrigDst(*net.UDPConn, []byte, []byte) (int, *net.UDPAddr, *net.UDPAddr, error) {
	return 0, nil, nil, errLinuxOnly
}

func writeSpoofedUDP(*net.UDPConn, *net.UDPAddr, *net.UDPAddr, []byte) error { return errLinuxOnly }
