//go:build linux

package caps

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Missing reports which of the wanted capabilities are NOT in the process's
// PERMITTED set — i.e. the ones Ensure could not raise. It reads caps without
// changing them, so a preflight can aggregate every unmet requirement into one
// report before failing. If capget itself fails, every wanted cap is reported
// missing (fail-closed).
func Missing(want Set) Set {
	if want.Empty() {
		return Of()
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return want
	}
	var miss []Cap
	for _, c := range want.List() {
		if int(c) < 0 || int(c) >= 64 {
			miss = append(miss, c)
			continue
		}
		idx, bit := int(c)/32, uint(int(c)%32)
		if data[idx].Permitted&(uint32(1)<<bit) == 0 {
			miss = append(miss, c)
		}
	}
	return Of(miss...)
}

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
		if int(c) < 0 || int(c) >= 64 {
			return fmt.Errorf("caps: capability %d out of range (v3 covers 0..63)", int(c))
		}
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
