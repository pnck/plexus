package bwrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ExtractBwrap must be content-addressed and reused: a sandboxed process execs into the
// extracted binary and cannot clean it up, so a per-launch temp file would leak. Two
// calls must return the same cached, executable path under ~/.plexus/cache/bwrap.
func TestExtractBwrapCachesAndIsExecutable(t *testing.T) {
	if len(bwrapBinary) == 0 {
		t.Skip("no embedded bwrap on this platform")
	}
	t.Setenv("HOME", t.TempDir())

	p1, err := ExtractBwrap()
	if err != nil {
		t.Fatalf("ExtractBwrap: %v", err)
	}
	p2, err := ExtractBwrap()
	if err != nil {
		t.Fatalf("ExtractBwrap (2nd): %v", err)
	}
	if p1 != p2 {
		t.Fatalf("cache miss: %q != %q — extraction must be content-addressed and reused", p1, p2)
	}
	if !strings.Contains(p1, filepath.Join(".plexus", "cache", "bwrap")) {
		t.Fatalf("expected a ~/.plexus/cache/bwrap path, got %q", p1)
	}
	fi, err := os.Stat(p1)
	if err != nil {
		t.Fatalf("stat %s: %v", p1, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("%s is not executable (mode %v)", p1, fi.Mode())
	}
	if fi.Size() != int64(len(bwrapBinary)) {
		t.Fatalf("%s size %d != embedded %d", p1, fi.Size(), len(bwrapBinary))
	}
}

// When HOME is unusable the extractor must still produce a working (temp) binary rather
// than failing — sandboxing degrades to the leak, it does not break.
func TestExtractBwrapFallsBackWithoutHome(t *testing.T) {
	if len(bwrapBinary) == 0 {
		t.Skip("no embedded bwrap on this platform")
	}
	// Point HOME at a file so MkdirAll under it fails; ExtractBwrap must fall back to temp.
	bad := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", bad)

	p, err := ExtractBwrap()
	if err != nil {
		t.Fatalf("ExtractBwrap fallback: %v", err)
	}
	defer os.Remove(p)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("%s is not executable", p)
	}
}
