package netpol

import (
	"reflect"
	"strings"
	"testing"
)

var testParams = Params{CP: "10.0.0.1", BusPort: 4222, EgressPort: 1080, Mark: 0x1, Table: 100, MaxConns: 64}

// mustNFT fails the test if generation errors — the golden cases all use valid Params.
func mustNFT(t *testing.T, p NetPolicy, pr Params) string {
	t.Helper()
	got, err := GenerateNFT(p, pr)
	if err != nil {
		t.Fatalf("GenerateNFT: %v", err)
	}
	return got
}

// Golden: the generator is a pure function; lock its output so a rule change is a
// visible diff. Covers redirect (mark for TPROXY) + a logged drop.
func TestGenerateNFT_RedirectTCP_DropUDP_LogAll(t *testing.T) {
	got := mustNFT(t, NetPolicy{TCP: Redirect, UDP: Drop, Log: LogAll}, testParams)
	want := `table inet mesh {
  chain out {
    type filter hook output priority 0; policy drop;
    ip daddr 127.0.0.0/8 accept
    ip daddr 10.0.0.1 tcp dport 4222 accept
    ct state established,related accept
    ct state new meta l4proto { tcp, udp } ct count over 64 drop
    meta l4proto tcp log prefix "egress-tcp " meta mark set 0x1 accept
    meta l4proto udp log prefix "egress-udp " drop
  }
}
`
	if got != want {
		t.Fatalf("nft mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Deny-all (zero policy, log off) emits only the base rules — the two protocols
// fall through to `policy drop`.
func TestGenerateNFT_DenyAll(t *testing.T) {
	got := mustNFT(t, NetPolicy{}, testParams)
	want := `table inet mesh {
  chain out {
    type filter hook output priority 0; policy drop;
    ip daddr 127.0.0.0/8 accept
    ip daddr 10.0.0.1 tcp dport 4222 accept
    ct state established,related accept
    ct state new meta l4proto { tcp, udp } ct count over 64 drop
  }
}
`
	if got != want {
		t.Fatalf("deny-all nft mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// reject -> RST for TCP, ICMP reject for UDP; MaxConns=0 omits the ct-count line.
func TestGenerateNFT_Reject_NoCap(t *testing.T) {
	pr := testParams
	pr.MaxConns = 0
	got := mustNFT(t, NetPolicy{TCP: Reject, UDP: Reject}, pr)
	want := `table inet mesh {
  chain out {
    type filter hook output priority 0; policy drop;
    ip daddr 127.0.0.0/8 accept
    ip daddr 10.0.0.1 tcp dport 4222 accept
    ct state established,related accept
    meta l4proto tcp reject with tcp reset
    meta l4proto udp reject
  }
}
`
	if got != want {
		t.Fatalf("reject nft mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// ip-rule/route reroute is emitted only when something is redirected.
func TestGenerateIPRules(t *testing.T) {
	rules, err := GenerateIPRules(NetPolicy{TCP: Redirect}, testParams)
	if err != nil {
		t.Fatalf("GenerateIPRules: %v", err)
	}
	want := []string{
		"ip rule add fwmark 0x1 lookup 100",
		"ip route add local default dev lo table 100",
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("ip rules=%v want %v", rules, want)
	}
	if r, err := GenerateIPRules(NetPolicy{TCP: Reject, UDP: Drop}, testParams); err != nil || r != nil {
		t.Fatalf("no-redirect policy must emit no reroute, got %v err %v", r, err)
	}
}

// Generation is fail-closed: a CP that is not a bare IPv4 address is rejected, so no
// attacker-controlled string can inject nft rules and defeat the deny-all fence.
func TestGenerateNFT_RejectsCPInjection(t *testing.T) {
	for _, bad := range []string{
		"1.2.3.4 accept\n    ip daddr 0.0.0.0/0", // newline -> extra allow-all rule
		"1.2.3.4 accept",                          // whitespace -> trailing tokens
		"1.2.3.4}",                                // brace -> break out of the chain/table
		"evil.example.com",                        // hostname, not an IP
		"",                                        // empty
		"::1",                                     // IPv6 (rule family is ip/IPv4)
	} {
		if _, err := GenerateNFT(NetPolicy{TCP: Redirect}, Params{CP: bad, BusPort: 4222, Mark: 1, Table: 100}); err == nil {
			t.Fatalf("GenerateNFT must reject CP %q", bad)
		}
		if _, err := GenerateIPRules(NetPolicy{TCP: Redirect}, Params{CP: bad, BusPort: 4222, Mark: 1, Table: 100}); err == nil {
			t.Fatalf("GenerateIPRules must reject CP %q", bad)
		}
	}
	// A malicious CP never reaches the output — no injected token leaks through.
	if out, err := GenerateNFT(NetPolicy{TCP: Redirect}, Params{CP: "1.2.3.4 accept\n x", BusPort: 4222, Mark: 1, Table: 100}); err == nil || strings.Contains(out, "0.0.0.0") {
		t.Fatalf("injection leaked: err=%v out=%q", err, out)
	}
}

// Numeric-field range checks (interpolated as %d, so not an injection vector, but a
// misconfiguration must fail closed rather than emit a broken ruleset).
func TestParamsValidate_Ranges(t *testing.T) {
	for _, tc := range []struct {
		name string
		pr   Params
	}{
		{"bus port zero", Params{CP: "10.0.0.1", BusPort: 0}},
		{"bus port high", Params{CP: "10.0.0.1", BusPort: 70000}},
		{"negative maxconns", Params{CP: "10.0.0.1", BusPort: 4222, MaxConns: -1}},
	} {
		if err := tc.pr.Validate(); err == nil {
			t.Fatalf("%s: Validate must fail", tc.name)
		}
	}
	if err := testParams.Validate(); err != nil {
		t.Fatalf("valid Params rejected: %v", err)
	}
}
