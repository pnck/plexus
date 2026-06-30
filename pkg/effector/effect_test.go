package effector

import "testing"

func TestEffectSetOps(t *testing.T) {
	rw := NewEffectSet(FSRead, FSWrite)
	if !rw.Contains(FSRead) || !rw.Contains(FSWrite) {
		t.Fatal("Contains missed a member")
	}
	if rw.Contains(ExecArbitrary) {
		t.Fatal("Contains reported a non-member")
	}
	// ∅ is a subset of everything; everything is a subset of itself.
	if !NewEffectSet().SubsetOf(rw) {
		t.Fatal("empty set should be a subset")
	}
	if !rw.SubsetOf(rw) {
		t.Fatal("set should be a subset of itself")
	}
	// fs.write alone ⊆ {read,write}; exec.arbitrary ⊄ {read,write}.
	if !NewEffectSet(FSWrite).SubsetOf(rw) {
		t.Fatal("{fs.write} should be ⊆ {fs.read,fs.write}")
	}
	if NewEffectSet(ExecArbitrary).SubsetOf(rw) {
		t.Fatal("{exec.arbitrary} must NOT be ⊆ {fs.read,fs.write}")
	}
	if got := NewEffectSet(FSRead).Union(NewEffectSet(FSWrite)); got != rw {
		t.Fatalf("Union=%v want %v", got, rw)
	}
	if NewEffectSet().String() != "∅" {
		t.Fatalf("empty String=%q want ∅", NewEffectSet().String())
	}
	if got := rw.String(); got != "fs.read,fs.write" {
		t.Fatalf("String=%q want fs.read,fs.write", got)
	}
}

func TestParseEffect(t *testing.T) {
	for name, want := range map[string]EffectSet{
		"fs.read":        FSRead,
		"exec.arbitrary": ExecArbitrary,
		"secret.read":    SecretRead,
	} {
		got, err := ParseEffect(name)
		if err != nil || got != want {
			t.Fatalf("ParseEffect(%q)=%v,%v want %v", name, got, err, want)
		}
		// Round-trip: the canonical name parses back to itself.
		if want.String() != name {
			t.Fatalf("round-trip %v.String()=%q want %q", want, want.String(), name)
		}
	}
	if _, err := ParseEffect("fs.teleport"); err == nil {
		t.Fatal("a name outside the closed vocabulary should error")
	}
}

func TestParseEffectSet(t *testing.T) {
	if got, err := ParseEffectSet(nil); err != nil || !got.IsEmpty() {
		t.Fatalf("ParseEffectSet(nil)=%v,%v want ∅", got, err)
	}
	got, err := ParseEffectSet([]string{"fs.read", "exec.boxed", "secret.read"})
	if err != nil {
		t.Fatalf("ParseEffectSet: %v", err)
	}
	if want := NewEffectSet(FSRead, ExecBoxed, SecretRead); got != want {
		t.Fatalf("ParseEffectSet=%v want %v", got, want)
	}
	if _, err := ParseEffectSet([]string{"fs.read", "bogus"}); err == nil {
		t.Fatal("an unknown name in the list should error")
	}
}

func TestPermittedPolicy(t *testing.T) {
	// A role permitted only to read: writes and exec escalate; reads auto-allow;
	// ∅ (internal cognition) always auto-allows.
	p := PermittedPolicy{Permitted: NewEffectSet(FSRead)}
	read := fakeEffector{name: "read_file", effects: NewEffectSet(FSRead)}
	write := fakeEffector{name: "write_file", effects: NewEffectSet(FSWrite)}
	internal := fakeEffector{name: "mem_write"} // ∅
	if p.RequiresApproval(read) {
		t.Fatal("read ⊆ permitted should auto-allow")
	}
	if !p.RequiresApproval(write) {
		t.Fatal("write ⊄ permitted should require approval")
	}
	if p.RequiresApproval(internal) {
		t.Fatal("∅ effects should always auto-allow")
	}
	// DefaultPermitted preserves the pre-Effect behavior: write auto, arbitrary gated.
	if (DefaultPolicy{}).RequiresApproval(write) {
		t.Fatal("DefaultPolicy: fs.write should auto-allow")
	}
	if !(DefaultPolicy{}).RequiresApproval(fakeEffector{name: "run_command", effects: NewEffectSet(ExecArbitrary)}) {
		t.Fatal("DefaultPolicy: exec.arbitrary should require approval")
	}
}
