package netpol

import (
	"strings"
	"testing"
)

// A role card with no `network` (empty policy) is deny-all + unaudited — the safe
// default; a partial policy defaults the unspecified fields the same way.
func TestParseDefaults(t *testing.T) {
	empty, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	if empty != (NetPolicy{}) || empty.TCP != Drop || empty.UDP != Drop || empty.Log != LogOff {
		t.Fatalf("empty policy must be deny-all/off, got %+v", empty)
	}

	// Only tcp specified -> udp stays Drop, log stays Off.
	p, err := Parse([]byte("tcp: redirect\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.TCP != Redirect || p.UDP != Drop || p.Log != LogOff {
		t.Fatalf("partial policy defaults wrong: %+v", p)
	}
}

func TestParseActionsAndLog(t *testing.T) {
	p, err := Parse([]byte("tcp: reject\nudp: redirect\nlog: tcp_only\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.TCP != Reject || p.UDP != Redirect || p.Log != LogTCPOnly {
		t.Fatalf("actions/log not parsed: %+v", p)
	}

	// Invalid tokens are rejected at parse time.
	if _, err := Parse([]byte("tcp: allow\n")); err == nil {
		t.Fatal("invalid action must error")
	}
	if _, err := Parse([]byte("log: sometimes\n")); err == nil {
		t.Fatal("invalid log must error")
	}
}

func TestDecideAndLogs(t *testing.T) {
	p := NetPolicy{TCP: Redirect, UDP: Drop, Log: LogUDPOnly}
	if p.Decide(TCP) != Redirect || p.Decide(UDP) != Drop {
		t.Fatalf("Decide wrong: tcp=%v udp=%v", p.Decide(TCP), p.Decide(UDP))
	}
	if p.logs(TCP) {
		t.Fatal("udp_only must not log tcp")
	}
	if !p.logs(UDP) {
		t.Fatal("udp_only must log udp")
	}
}

// Describe renders LLM-facing prose (for the env-state L1 frame): it surfaces the
// concrete limits, never the mechanism.
func TestDescribe(t *testing.T) {
	cases := map[string]struct {
		p    NetPolicy
		want string
	}{
		"deny-all":    {NetPolicy{}, "Outbound network (TCP and UDP): blocked."},
		"all-via-cp":  {NetPolicy{TCP: Redirect, UDP: Redirect}, "Outbound network (TCP and UDP): allowed, routed through the control-plane egress proxy (all traffic is audited)."},
		"tcp-only":    {NetPolicy{TCP: Redirect, UDP: Drop}, "Outbound network — TCP: allowed, routed through the control-plane egress proxy (all traffic is audited); UDP: blocked."},
		"tcp-refused": {NetPolicy{TCP: Reject, UDP: Drop}, "Outbound network — TCP: refused; UDP: blocked."},
	}
	for name, c := range cases {
		if got := c.p.Describe(); got != c.want {
			t.Fatalf("%s: Describe()=%q\nwant %q", name, got, c.want)
		}
		if strings.Contains(c.p.Describe(), "tproxy") || strings.Contains(c.p.Describe(), "nft") {
			t.Fatalf("%s: Describe must not leak the mechanism: %q", name, c.p.Describe())
		}
	}
}

// String round-trips keep the config vocabulary stable.
func TestStrings(t *testing.T) {
	for act, want := range map[NetAction]string{Drop: "drop", Reject: "reject", Redirect: "redirect"} {
		if act.String() != want {
			t.Fatalf("NetAction(%d).String()=%q want %q", act, act.String(), want)
		}
	}
	for l, want := range map[LogScope]string{LogOff: "off", LogAll: "all", LogTCPOnly: "tcp_only", LogUDPOnly: "udp_only"} {
		if l.String() != want {
			t.Fatalf("LogScope(%d).String()=%q want %q", l, l.String(), want)
		}
	}
}
