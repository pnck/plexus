//go:build linux

package fence

import (
	"strings"
	"testing"

	"github.com/google/nftables"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"

	"plexus/sandbox/netpol"
)

// applyNFT (the programmatic builder that actually programs the kernel) must stay in step
// with netpol.GenerateNFT (the golden text fence_test locks). The confined agent is
// cap-dropped and cannot read the applied ruleset back, so the lock has to be at build
// time: drive buildNFT with a recording netlink dial and assert it emits exactly as many
// rules as the golden — no kernel, no caps.
func TestBuildNFTMatchesGoldenRuleCount(t *testing.T) {
	cases := []struct {
		name string
		pol  netpol.NetPolicy
		par  netpol.Params
	}{
		{"deny-all", netpol.NetPolicy{}, netpol.Params{CP: "10.242.42.1", BusPort: 4222, Mark: 1, Table: 100}},
		{"redirect-both+relay+ctcount+log", netpol.NetPolicy{TCP: netpol.Redirect, UDP: netpol.Redirect, Log: netpol.LogAll}, netpol.Params{CP: "10.242.42.1", BusPort: 4222, RelayPort: 1080, Mark: 1, Table: 100, MaxConns: 64}},
		{"reject-tcp-drop-udp", netpol.NetPolicy{TCP: netpol.Reject, UDP: netpol.Drop}, netpol.Params{CP: "10.242.42.1", BusPort: 4222, Mark: 1, Table: 100}},
		{"redirect-tcp-log-tcp-only", netpol.NetPolicy{TCP: netpol.Redirect, UDP: netpol.Drop, Log: netpol.LogTCPOnly}, netpol.Params{CP: "10.242.42.1", BusPort: 4222, RelayPort: 1080, Mark: 1, Table: 100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			golden, err := netpol.GenerateNFT(tc.pol, tc.par)
			if err != nil {
				t.Fatalf("golden: %v", err)
			}
			want := countGoldenRules(golden)
			got := countEmittedRules(t, tc.pol, tc.par)
			if got != want {
				t.Errorf("buildNFT emitted %d rules, golden text has %d — the two representations drifted\n%s", got, want, golden)
			}
		})
	}
}

// countEmittedRules runs buildNFT against a recording (kernel-less) netlink dial and
// counts the NEWRULE messages it flushes.
func countEmittedRules(t *testing.T, pol netpol.NetPolicy, par netpol.Params) int {
	t.Helper()
	const newRule = netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES << 8) | unix.NFT_MSG_NEWRULE)
	n := 0
	c, err := nftables.New(nftables.WithTestDial(func(req []netlink.Message) ([]netlink.Message, error) {
		for _, m := range req {
			if m.Header.Type == newRule {
				n++
			}
		}
		return req, nil
	}))
	if err != nil {
		t.Fatalf("nftables test conn: %v", err)
	}
	if err := buildNFT(c, pol, par); err != nil {
		t.Fatalf("buildNFT: %v", err)
	}
	return n
}

// countGoldenRules counts the rule lines in a GenerateNFT ruleset: everything indented
// inside `chain out { … }` except the `type … policy drop;` chain header.
func countGoldenRules(nft string) int {
	n := 0
	for _, line := range strings.Split(nft, "\n") {
		s := strings.TrimSpace(line)
		switch {
		case s == "", s == "}":
		case strings.HasPrefix(s, "table "), strings.HasPrefix(s, "chain "), strings.HasPrefix(s, "type filter"):
		default:
			n++
		}
	}
	return n
}
