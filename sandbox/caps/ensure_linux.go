//go:build linux

package caps

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Ensure raises every capability in want from the process's PERMITTED set into its
// EFFECTIVE set, so the privileged setup calls succeed. It is fail-closed: if a
// wanted capability is not permitted, it changes nothing and returns an error naming
// exactly what is missing, so the operator knows what to grant (--cap-add=… /
// setcap / run the setup stage as root). It only ever RAISES already-permitted caps
// — it never grants what the kernel withheld, and never touches unrelated caps.
func Ensure(want Set) error {
	if want.Empty() {
		return nil
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return fmt.Errorf("caps: capget: %w", err)
	}
	var missing []Cap
	for _, c := range want.List() {
		idx, bit := int(c)/32, uint(int(c)%32)
		if data[idx].Permitted&(uint32(1)<<bit) == 0 {
			missing = append(missing, c)
			continue
		}
		data[idx].Effective |= uint32(1) << bit
	}
	if len(missing) > 0 {
		return fmt.Errorf("caps: %s not permitted — grant via --cap-add / setcap / root", Of(missing...).Describe())
	}
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return fmt.Errorf("caps: capset: %w", err)
	}
	return nil
}
