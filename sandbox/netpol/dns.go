package netpol

import (
	"fmt"
	"net"
	"strings"
)

// ResolvConf renders an /etc/resolv.conf that forces DNS-over-TCP (`options
// use-vc`), so a role with `udp: drop` still resolves names — DNS then rides the
// tcp axis through the egress fence instead of native UDP (E4.6.6, flow doc §6.2).
// Setup provisions this file into the sandbox.
//
// Fail-closed, mirroring GenerateNFT: at least one nameserver is required, and each
// must be a bare IP address — resolv.conf is parsed by libc, so a value carrying a
// newline or stray tokens could otherwise inject directives.
func ResolvConf(nameservers []string) (string, error) {
	if len(nameservers) == 0 {
		return "", fmt.Errorf("netpol: resolv.conf needs at least one nameserver")
	}
	var b strings.Builder
	b.WriteString("# plexus: DNS over TCP so a udp:drop role still resolves (flow doc §6.2)\n")
	b.WriteString("options use-vc\n")
	for _, ns := range nameservers {
		if net.ParseIP(ns) == nil {
			return "", fmt.Errorf("netpol: nameserver %q is not an IP address", ns)
		}
		fmt.Fprintf(&b, "nameserver %s\n", ns)
	}
	return b.String(), nil
}
