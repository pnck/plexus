//go:build linux

package setup

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// OSExecutor is the real privileged Phase-0 Executor (E4.6.4.2). It builds the
// per-agent network objects with iproute2 (`ip`) and nftables (`nft`) — feeding
// `nft -f` the exact text netpol.GenerateNFT produced and replaying the `ip …`
// commands netpol.GenerateIPRules produced, so the applied fence IS the golden,
// unit-tested ruleset — writes the cgroup-v2 limit files directly, and finally
// joins the netns + cgroup and execs the agent. It requires CAP_NET_ADMIN and a
// writable cgroup subtree (Phase 0 runs as the privileged Setup, flow doc §7).
//
// This shells iproute2/nftables rather than driving netlink / google-nftables
// programmatically: it reuses the golden ruleset text as the single source of
// truth, adds no Go dependencies, and uses standard tooling — at the cost of
// requiring `ip` and `nft` in the Setup environment.
type OSExecutor struct{}

// run executes a command, surfacing stderr on failure.
func run(name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ipIn runs an `ip` subcommand, inside netns when non-empty.
func ipIn(netns string, args ...string) error {
	if netns == "" {
		return run("ip", args...)
	}
	return run("ip", append([]string{"netns", "exec", netns, "ip"}, args...)...)
}

func (OSExecutor) CreateNetns(name string) error {
	return run("ip", "netns", "add", name)
}

func (OSExecutor) CreateVethPair(host, peer string) error {
	return run("ip", "link", "add", host, "type", "veth", "peer", "name", peer)
}

func (OSExecutor) MoveToNetns(iface, netns string) error {
	return run("ip", "link", "set", iface, "netns", netns)
}

func (OSExecutor) SetAddr(netns, iface, cidr string) error {
	return ipIn(netns, "addr", "add", cidr, "dev", iface)
}

func (OSExecutor) SetLinkUp(netns, iface string) error {
	return ipIn(netns, "link", "set", iface, "up")
}

func (OSExecutor) AddDefaultRoute(netns, gateway string) error {
	return ipIn(netns, "route", "add", "default", "via", gateway)
}

// ApplyNFT pipes the generated ruleset text to `nft -f -` inside the netns (a single
// atomic nftables transaction).
func (OSExecutor) ApplyNFT(netns, ruleset string) error {
	var argv []string
	if netns == "" {
		argv = []string{"nft", "-f", "-"}
	} else {
		argv = []string{"ip", "netns", "exec", netns, "nft", "-f", "-"}
	}
	var stderr bytes.Buffer
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(ruleset)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nft -f: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// AddIPRules replays each generated `ip …` command inside the netns (the TPROXY
// reroute: `ip rule add fwmark …` + `ip route add local …`).
func (OSExecutor) AddIPRules(netns string, rules []string) error {
	for _, r := range rules {
		f := strings.Fields(r)
		if len(f) < 2 || f[0] != "ip" {
			return fmt.Errorf("setup: malformed ip rule %q", r)
		}
		if err := ipIn(netns, f[1:]...); err != nil {
			return err
		}
	}
	return nil
}

// CreateCgroup makes a cgroup-v2 group and writes its limits. A zero limit is left
// at the parent's default.
func (OSExecutor) CreateCgroup(name string, lim CgroupLimits) error {
	base := "/sys/fs/cgroup/" + name
	if err := os.MkdirAll(base, 0o755); err != nil {
		return fmt.Errorf("setup: mkdir cgroup %s: %w", base, err)
	}
	write := func(file, val string) error {
		if val == "" {
			return nil
		}
		if err := os.WriteFile(base+"/"+file, []byte(val), 0o644); err != nil {
			return fmt.Errorf("setup: write %s/%s: %w", base, file, err)
		}
		return nil
	}
	if lim.MemoryMax > 0 {
		if err := write("memory.max", strconv.FormatInt(lim.MemoryMax, 10)); err != nil {
			return err
		}
	}
	if lim.PidsMax > 0 {
		if err := write("pids.max", strconv.FormatInt(lim.PidsMax, 10)); err != nil {
			return err
		}
	}
	return write("cpu.max", lim.CPUMax)
}

// EnterAndExec joins the netns and cgroup, then execs the agent — replacing this
// process, so its parent stays the caller (the persistent CP). The OS thread is
// locked so the netns setns applies to the thread that immediately execs.
func (OSExecutor) EnterAndExec(netns, cgroup string, argv, env []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("setup: empty argv")
	}
	runtime.LockOSThread()

	fd, err := unix.Open("/var/run/netns/"+netns, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("setup: open netns %s: %w", netns, err)
	}
	if err := unix.Setns(fd, unix.CLONE_NEWNET); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("setup: setns %s: %w", netns, err)
	}
	_ = unix.Close(fd)

	if cgroup != "" {
		procs := "/sys/fs/cgroup/" + cgroup + "/cgroup.procs"
		if err := os.WriteFile(procs, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			return fmt.Errorf("setup: join cgroup %s: %w", cgroup, err)
		}
	}

	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("setup: lookpath %s: %w", argv[0], err)
	}
	return syscall.Exec(bin, argv, env)
}
