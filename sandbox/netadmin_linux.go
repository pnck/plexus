//go:build linux

package sandbox

import "golang.org/x/sys/unix"

// hasNetAdmin reports whether this process holds CAP_NET_ADMIN in its EFFECTIVE set —
// the prerequisite for building the network fence (veth + nft + TPROXY). It reads caps
// without changing them (capget). The documented grant is `setcap cap_net_admin+ep`
// (permitted+effective) / root / --cap-add=NET_ADMIN, all of which land it effective;
// absent → the network fence degrades (§ sandbox.Enter, implement-design §5.6.9).
func hasNetAdmin() bool {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return false
	}
	const c = unix.CAP_NET_ADMIN // 12
	return data[c/32].Effective&(uint32(1)<<(uint(c)%32)) != 0
}
