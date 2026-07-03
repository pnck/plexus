package netpol

import (
	"reflect"
	"testing"
)

var testParams = Params{CP: "10.0.0.1", BusPort: 4222, EgressPort: 1080, Mark: 0x1, Table: 100, MaxConns: 64}

// Golden: the generator is a pure function; lock its output so a rule change is a
// visible diff. Covers redirect (mark for TPROXY) + a logged drop.
func TestGenerateNFT_RedirectTCP_DropUDP_LogAll(t *testing.T) {
	got := GenerateNFT(NetPolicy{TCP: Redirect, UDP: Drop, Log: LogAll}, testParams)
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
	got := GenerateNFT(NetPolicy{}, testParams)
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
	got := GenerateNFT(NetPolicy{TCP: Reject, UDP: Reject}, pr)
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
	rules := GenerateIPRules(NetPolicy{TCP: Redirect}, testParams)
	want := []string{
		"ip rule add fwmark 0x1 lookup 100",
		"ip route add local default dev lo table 100",
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("ip rules=%v want %v", rules, want)
	}
	if r := GenerateIPRules(NetPolicy{TCP: Reject, UDP: Drop}, testParams); r != nil {
		t.Fatalf("no-redirect policy must emit no reroute, got %v", r)
	}
}
