package caps

import "testing"

func TestSetDedupOrderDescribe(t *testing.T) {
	s := Of(NetAdmin, SysAdmin, NetAdmin) // duplicate dropped
	if len(s.List()) != 2 {
		t.Fatalf("dedup failed: %v", s.List())
	}
	if !s.Has(NetAdmin) || s.Has(BPF) {
		t.Fatal("Has wrong")
	}
	// Stable numeric order: NetAdmin(12) < SysAdmin(21).
	if got := s.Describe(); got != "CAP_NET_ADMIN, CAP_SYS_ADMIN" {
		t.Fatalf("Describe = %q", got)
	}
	if !Of().Empty() || Of().Describe() != "none" {
		t.Fatal("empty set")
	}
}

type stub struct{ s Set }

func (r stub) RequiredCaps() Set { return r.s }

// Collect unions every participant's needs and skips nil (visitor pattern).
func TestCollect(t *testing.T) {
	got := Collect(stub{Of(NetAdmin)}, nil, stub{Of(SysAdmin, BPF)})
	if got.Describe() != "CAP_NET_ADMIN, CAP_SYS_ADMIN, CAP_BPF" {
		t.Fatalf("Collect = %q", got.Describe())
	}
}
