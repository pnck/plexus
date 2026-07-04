// Package egress is the agent-side transparent egress proxy (E4.6.1–3) that runs
// inside the per-agent netns: every allowed outbound flow (TCP/UDP) is TPROXY-
// intercepted to a local port here, attributed to the originating process, judged
// against the NetPolicy, and relayed to the control-plane EgressRelay. This file is
// the user-space attribution tier (flow doc §6.8 tier ①): map a flow to its process
// via /proc. It is pure stdlib and degrades to "no owner" off Linux.
package egress

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Attribution names the process behind an egress flow.
type Attribution struct {
	PID  int
	Comm string
}

// Attribute maps an egress flow — identified by its originating socket's local
// address (src:port, as the agent sees it) — to the owning process: find the socket
// inode in /proc/net/<proto>, then scan /proc/*/fd for that inode. Best-effort and
// slightly racy for short-lived flows, but egress connections are few and
// long-lived, so this is enough for audit (flow doc §6.4/§6.8 tier ①; the precise,
// race-free tier is cgroup-BPF, E4.6.3.2). Returns a zero Attribution (PID 0) when
// no owner is found — the flow already closed.
func Attribute(proto string, srcIP net.IP, srcPort int) (Attribution, error) {
	inode, err := socketInode(proto, srcIP, srcPort)
	if err != nil || inode == 0 {
		return Attribution{}, err
	}
	pid, err := pidForInode(inode)
	if err != nil || pid == 0 {
		return Attribution{}, err
	}
	return Attribution{PID: pid, Comm: readComm(pid)}, nil
}

func procNetFiles(proto string) []string {
	if proto == "udp" {
		return []string{"/proc/net/udp", "/proc/net/udp6"}
	}
	return []string{"/proc/net/tcp", "/proc/net/tcp6"}
}

func socketInode(proto string, srcIP net.IP, srcPort int) (uint64, error) {
	for _, f := range procNetFiles(proto) {
		inode, ok, err := scanProcNet(f, srcIP, srcPort)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		if ok {
			return inode, nil
		}
	}
	return 0, nil
}

func scanProcNet(path string, srcIP net.IP, srcPort int) (uint64, bool, error) {
	fh, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Scan() // header row
	for sc.Scan() {
		ip, port, inode, ok := parseProcNetLine(sc.Text())
		if !ok || port != srcPort {
			continue
		}
		if srcIP == nil || srcIP.IsUnspecified() || ip.Equal(srcIP) {
			return inode, true, nil
		}
	}
	return 0, false, sc.Err()
}

// parseProcNetLine extracts the local address + socket inode from one /proc/net/
// {tcp,udp} row: "sl local_address rem_address st ... uid timeout inode ...".
func parseProcNetLine(line string) (net.IP, int, uint64, bool) {
	f := strings.Fields(line)
	if len(f) < 10 {
		return nil, 0, 0, false
	}
	ip, port, ok := parseHexAddr(f[1])
	if !ok {
		return nil, 0, 0, false
	}
	inode, err := strconv.ParseUint(f[9], 10, 64)
	if err != nil {
		return nil, 0, 0, false
	}
	return ip, port, inode, true
}

// parseHexAddr decodes a /proc/net "IIIIIIII:PPPP" local_address. The IP is written
// in host byte order per 32-bit word (little-endian on x86/arm), so each 4-byte
// group is reversed; the port is big-endian hex.
func parseHexAddr(s string) (net.IP, int, bool) {
	h, p, found := strings.Cut(s, ":")
	if !found {
		return nil, 0, false
	}
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil, 0, false
	}
	ip := make(net.IP, len(raw))
	for w := 0; w < len(raw); w += 4 {
		ip[w+0], ip[w+1], ip[w+2], ip[w+3] = raw[w+3], raw[w+2], raw[w+1], raw[w+0]
	}
	port, err := strconv.ParseUint(p, 16, 32)
	if err != nil {
		return nil, 0, false
	}
	return ip, int(port), true
}

// pidForInode scans /proc/*/fd for a symlink to socket:[inode]; first owner wins.
func pidForInode(inode uint64) (int, error) {
	target := fmt.Sprintf("socket:[%d]", inode)
	fds, err := filepath.Glob("/proc/[0-9]*/fd/*")
	if err != nil {
		return 0, err
	}
	for _, fd := range fds {
		if link, err := os.Readlink(fd); err == nil && link == target {
			parts := strings.Split(fd, "/") // ["", "proc", "<pid>", "fd", "<n>"]
			if len(parts) >= 3 {
				if pid, err := strconv.Atoi(parts[2]); err == nil {
					return pid, nil
				}
			}
		}
	}
	return 0, nil
}

func readComm(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
