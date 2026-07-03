package netpol

import (
	"strings"
	"testing"
)

// ResolvConf forces DNS-over-TCP and lists the given nameservers.
func TestResolvConf(t *testing.T) {
	got, err := ResolvConf([]string{"10.0.0.1", "1.1.1.1"})
	if err != nil {
		t.Fatalf("ResolvConf: %v", err)
	}
	for _, want := range []string{"options use-vc", "nameserver 10.0.0.1", "nameserver 1.1.1.1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("resolv.conf missing %q:\n%s", want, got)
		}
	}
}

// Fail-closed: no nameserver, or a value that is not a bare IP (a resolv.conf
// injection vector), is rejected.
func TestResolvConfFailClosed(t *testing.T) {
	if _, err := ResolvConf(nil); err == nil {
		t.Fatal("empty nameservers must be rejected")
	}
	for _, bad := range []string{
		"nameserver 8.8.8.8\noptions rotate", // newline -> injected directive
		"1.1.1.1 evil",                       // stray tokens
		"not-an-ip",
		"",
	} {
		if _, err := ResolvConf([]string{bad}); err == nil {
			t.Fatalf("ResolvConf must reject nameserver %q", bad)
		}
	}
}
