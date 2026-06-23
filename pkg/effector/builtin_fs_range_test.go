package effector

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// read_file with offset/length returns exactly that byte slice; without them,
// the whole file (backward compatible).
func TestReadFileRange(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	// whole file
	if r := invoke(t, ReadFile(), `{"path":"`+p+`"}`); r.Content != "0123456789" {
		t.Fatalf("full read = %q", r.Content)
	}
	// [3, 3+4) = "3456"
	if r := invoke(t, ReadFile(), `{"path":"`+p+`","offset":3,"length":4}`); r.Content != "3456" {
		t.Fatalf("range read = %q, want 3456", r.Content)
	}
	// offset to end
	if r := invoke(t, ReadFile(), `{"path":"`+p+`","offset":7}`); r.Content != "789" {
		t.Fatalf("offset-to-end = %q, want 789", r.Content)
	}
	// length beyond EOF is clamped
	if r := invoke(t, ReadFile(), `{"path":"`+p+`","offset":8,"length":99}`); r.Content != "89" {
		t.Fatalf("clamped read = %q, want 89", r.Content)
	}
}

// search reports every match (not just one per line) with its byte offset, and
// those offsets round-trip into read_file.
func TestSearchReportsOffsets(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	content := "foo bar foo\nbaz foo\n" // "foo" at 0, 8, 16
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r := invoke(t, Search(), `{"pattern":"foo","path":"`+p+`"}`)
	if r.IsError {
		t.Fatalf("search error: %s", r.Content)
	}
	lines := strings.Split(strings.TrimSpace(r.Content), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 matches (all occurrences), got %d:\n%s", len(lines), r.Content)
	}
	// Each line is path:line:@offset: text — verify the offsets are 0, 8, 16 and
	// that reading length-3 at each offset yields "foo".
	wantOffsets := []int{0, 8, 16}
	re := regexp.MustCompile(`:@(\d+):`)
	for i, ln := range lines {
		m := re.FindStringSubmatch(ln)
		if m == nil {
			t.Fatalf("line %d missing @offset: %q", i, ln)
		}
		off, _ := strconv.Atoi(m[1])
		if off != wantOffsets[i] {
			t.Fatalf("match %d offset = %d, want %d", i, off, wantOffsets[i])
		}
		got := invoke(t, ReadFile(), `{"path":"`+p+`","offset":`+m[1]+`,"length":3}`)
		if got.Content != "foo" {
			t.Fatalf("read at offset %d = %q, want foo", off, got.Content)
		}
	}
}

// edit_file with a byte range targets one of several identical occurrences
// without expanding old_string — using an offset from search.
func TestEditFileRangeTargetsOneOccurrence(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	// three "foo"; we want to change only the middle one (offset 8).
	if err := os.WriteFile(p, []byte("foo bar foo baz foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without a range, it is not unique -> error.
	if r := invoke(t, EditFile(), `{"path":"`+p+`","old_string":"foo","new_string":"XXX"}`); !r.IsError || !strings.Contains(r.Content, "not unique") {
		t.Fatalf("unranged edit = %q isErr=%v, want not-unique", r.Content, r.IsError)
	}
	// Confine to [8, 11) — exactly the middle "foo".
	if r := invoke(t, EditFile(), `{"path":"`+p+`","old_string":"foo","new_string":"XXX","start":8,"end":11}`); r.IsError {
		t.Fatalf("ranged edit failed: %s", r.Content)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "foo bar XXX baz foo" {
		t.Fatalf("after ranged edit = %q", data)
	}
}

// edit_file reports the range when old_string is absent within it.
func TestEditFileRangeNotFound(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("foo bar foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	// "foo" exists in the file but not within [3, 7) ("bar").
	r := invoke(t, EditFile(), `{"path":"`+p+`","old_string":"foo","new_string":"X","start":3,"end":7}`)
	if !r.IsError || !strings.Contains(r.Content, "[3,7)") {
		t.Fatalf("out-of-range edit = %q isErr=%v, want range-scoped not-found", r.Content, r.IsError)
	}
}
