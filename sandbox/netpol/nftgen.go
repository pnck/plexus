package netpol

import (
	"fmt"
	"net"
	"strings"
)

// Params are the deployment values the nft / ip-rule generator needs (from E5's
// catalog): the control-plane address + bus port, the local egress (TPROXY) port
// plexus listens on, the fwmark + routing-table id for the local-output TPROXY
// reroute, and the per-agent concurrent-connection cap.
type Params struct {
	CP         string // control-plane address, e.g. "10.0.0.1"
	BusPort    int    // CP bus port (allowed direct, not proxied)
	EgressPort int    // local TPROXY port plexus's transparent proxy listens on
	Mark       uint32 // fwmark for the TPROXY reroute
	Table      int    // ip-rule / ip-route table id for the reroute
	MaxConns   int    // ct count over N (per-agent concurrent egress cap); 0 = no cap
}

// Validate rejects Params that would corrupt the generated ruleset. CP is the only
// free-form string interpolated into the nftables text, so it MUST be a bare IPv4
// address: a CP carrying whitespace, a newline, or nft metacharacters could inject
// arbitrary rules and defeat the deny-all fence. The other interpolated fields are
// numeric (int/uint32 formatted as %d/%x — no injection is possible) but BusPort
// and MaxConns are range-checked so a misconfiguration fails here rather than
// emitting a broken ruleset. Generation is fail-closed: GenerateNFT / GenerateIPRules
// call this first and return an error (never a silently-permissive fence) on failure.
func (pr Params) Validate() error {
	if ip := net.ParseIP(pr.CP); ip == nil || ip.To4() == nil {
		return fmt.Errorf("netpol: CP %q must be a bare IPv4 address", pr.CP)
	}
	if pr.BusPort < 1 || pr.BusPort > 65535 {
		return fmt.Errorf("netpol: BusPort %d out of range 1..65535", pr.BusPort)
	}
	if pr.MaxConns < 0 {
		return fmt.Errorf("netpol: MaxConns %d must be >= 0", pr.MaxConns)
	}
	return nil
}

// GenerateNFT lowers a NetPolicy into an nftables ruleset (deterministic text).
//
// Model (tracking/E4-establishment-flow.md §6.2): a single filter/output chain
// with `policy drop` (default deny-all), allowing loopback, the bus, and
// established flows; an optional per-agent `ct count` cap; then, per protocol, the
// granted action — `redirect` marks the packet for the TPROXY reroute (§6.9),
// `reject` refuses it, `drop` is left to fall through to the chain policy. The log
// scope adds `log` to a protocol's blocked/redirected line.
func GenerateNFT(p NetPolicy, pr Params) (string, error) {
	if err := pr.Validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("table inet mesh {\n")
	b.WriteString("  chain out {\n")
	b.WriteString("    type filter hook output priority 0; policy drop;\n")
	b.WriteString("    ip daddr 127.0.0.0/8 accept\n")
	fmt.Fprintf(&b, "    ip daddr %s tcp dport %d accept\n", pr.CP, pr.BusPort)
	b.WriteString("    ct state established,related accept\n")
	if pr.MaxConns > 0 {
		fmt.Fprintf(&b, "    ct state new meta l4proto { tcp, udp } ct count over %d drop\n", pr.MaxConns)
	}
	b.WriteString(protoRule(TCP, p, pr))
	b.WriteString(protoRule(UDP, p, pr))
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String(), nil
}

// protoRule emits the line(s) for one protocol per its action + log scope. A
// `drop` with logging off contributes nothing (the chain policy drops it).
func protoRule(proto Proto, p NetPolicy, pr Params) string {
	l4 := proto.String()
	logStmt := ""
	if p.logs(proto) {
		logStmt = fmt.Sprintf("log prefix \"egress-%s \" ", l4)
	}
	switch p.Decide(proto) {
	case Redirect:
		// allowed -> mark for the TPROXY reroute, delivered to the local egress port
		return fmt.Sprintf("    meta l4proto %s %smeta mark set 0x%x accept\n", l4, logStmt, pr.Mark)
	case Reject:
		verdict := "reject"
		if proto == TCP {
			verdict = "reject with tcp reset"
		}
		return fmt.Sprintf("    meta l4proto %s %s%s\n", l4, logStmt, verdict)
	default: // Drop
		if logStmt != "" {
			return fmt.Sprintf("    meta l4proto %s %sdrop\n", l4, logStmt)
		}
		return "" // falls through to chain policy drop
	}
}

// GenerateIPRules emits the policy routing that delivers TPROXY-marked local
// output back for local delivery to plexus's IP_TRANSPARENT egress socket (§6.9).
// It is only needed when at least one protocol is redirect; otherwise nil.
func GenerateIPRules(p NetPolicy, pr Params) ([]string, error) {
	if err := pr.Validate(); err != nil {
		return nil, err
	}
	if p.TCP != Redirect && p.UDP != Redirect {
		return nil, nil
	}
	return []string{
		fmt.Sprintf("ip rule add fwmark 0x%x lookup %d", pr.Mark, pr.Table),
		fmt.Sprintf("ip route add local default dev lo table %d", pr.Table),
	}, nil
}
