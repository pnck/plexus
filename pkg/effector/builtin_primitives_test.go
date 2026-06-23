package effector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"plexus/pkg/store"
)

func invoke(t *testing.T, e Effector, args string) Result {
	t.Helper()
	res, err := e.Invoke(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s.Invoke infra error: %v", e.Name(), err)
	}
	return res
}

func TestFilesystemRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.txt")

	if r := invoke(t, WriteFile(), `{"path":"`+p+`","content":"hello world"}`); r.IsError {
		t.Fatalf("write_file: %s", r.Content)
	}
	if r := invoke(t, ReadFile(), `{"path":"`+p+`"}`); r.IsError || r.Content != "hello world" {
		t.Fatalf("read_file = %q err=%v", r.Content, r.IsError)
	}
	// append mode
	if r := invoke(t, WriteFile(), `{"path":"`+p+`","content":"!","mode":"append"}`); r.IsError {
		t.Fatalf("write_file append: %s", r.Content)
	}
	if r := invoke(t, ReadFile(), `{"path":"`+p+`"}`); r.Content != "hello world!" {
		t.Fatalf("after append = %q", r.Content)
	}
	// edit_file (unique replacement)
	if r := invoke(t, EditFile(), `{"path":"`+p+`","old_string":"world","new_string":"plexus"}`); r.IsError {
		t.Fatalf("edit_file: %s", r.Content)
	}
	if r := invoke(t, ReadFile(), `{"path":"`+p+`"}`); r.Content != "hello plexus!" {
		t.Fatalf("after edit = %q", r.Content)
	}
	// stat
	if r := invoke(t, Stat(), `{"path":"`+p+`"}`); r.IsError || !strings.Contains(r.Content, `"exists":true`) {
		t.Fatalf("stat = %q", r.Content)
	}
	// list_dir + glob + search
	if r := invoke(t, ListDir(), `{"path":"`+dir+`"}`); !strings.Contains(r.Content, "note.txt") {
		t.Fatalf("list_dir = %q", r.Content)
	}
	if r := invoke(t, Glob(), `{"pattern":"`+filepath.Join(dir, "*.txt")+`"}`); !strings.Contains(r.Content, "note.txt") {
		t.Fatalf("glob = %q", r.Content)
	}
	if r := invoke(t, Search(), `{"pattern":"plexus","path":"`+dir+`"}`); r.IsError || !strings.Contains(r.Content, "note.txt:1:") {
		t.Fatalf("search = %q", r.Content)
	}
	// move then remove
	q := filepath.Join(dir, "renamed.txt")
	if r := invoke(t, MoveFile(), `{"source":"`+p+`","dest":"`+q+`"}`); r.IsError {
		t.Fatalf("move_file: %s", r.Content)
	}
	if r := invoke(t, RemoveFile(), `{"path":"`+q+`"}`); r.IsError {
		t.Fatalf("remove_file: %s", r.Content)
	}
	if _, err := os.Stat(q); !os.IsNotExist(err) {
		t.Fatalf("file still exists after remove_file")
	}
}

func TestStatAbsentAndMakeDir(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	if r := invoke(t, Stat(), `{"path":"`+missing+`"}`); r.IsError || !strings.Contains(r.Content, `"exists":false`) {
		t.Fatalf("stat absent = %q isErr=%v", r.Content, r.IsError)
	}
	sub := filepath.Join(dir, "a", "b", "c")
	if r := invoke(t, MakeDir(), `{"path":"`+sub+`"}`); r.IsError {
		t.Fatalf("make_dir: %s", r.Content)
	}
	if info, err := os.Stat(sub); err != nil || !info.IsDir() {
		t.Fatalf("make_dir did not create %s: %v", sub, err)
	}
}

func TestEditFileGuards(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("aa bb aa"), 0o644); err != nil {
		t.Fatal(err)
	}
	// not found
	if r := invoke(t, EditFile(), `{"path":"`+p+`","old_string":"zz","new_string":"x"}`); !r.IsError || !strings.Contains(r.Content, "not found") {
		t.Fatalf("edit not-found = %q isErr=%v", r.Content, r.IsError)
	}
	// not unique without replace_all
	if r := invoke(t, EditFile(), `{"path":"`+p+`","old_string":"aa","new_string":"x"}`); !r.IsError || !strings.Contains(r.Content, "not unique") {
		t.Fatalf("edit not-unique = %q isErr=%v", r.Content, r.IsError)
	}
	// replace_all succeeds
	if r := invoke(t, EditFile(), `{"path":"`+p+`","old_string":"aa","new_string":"x","replace_all":true}`); r.IsError {
		t.Fatalf("edit replace_all: %s", r.Content)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "x bb x" {
		t.Fatalf("after replace_all = %q", data)
	}
	// identical old/new rejected
	if r := invoke(t, EditFile(), `{"path":"`+p+`","old_string":"x","new_string":"x"}`); !r.IsError {
		t.Fatalf("edit identical: want error")
	}
}

func TestWriteFileRefusesDirectory(t *testing.T) {
	dir := t.TempDir()
	if r := invoke(t, WriteFile(), `{"path":"`+dir+`","content":"x"}`); !r.IsError || !strings.Contains(r.Content, "non-regular") {
		t.Fatalf("write to dir = %q isErr=%v, want refusal", r.Content, r.IsError)
	}
	if r := invoke(t, RemoveFile(), `{"path":"`+dir+`"}`); !r.IsError || !strings.Contains(r.Content, "directory") {
		t.Fatalf("remove dir = %q isErr=%v, want refusal", r.Content, r.IsError)
	}
}

func TestSysEffectors(t *testing.T) {
	t.Setenv("PLEXUS_TEST_VAR", "present")
	if r := invoke(t, GetEnv(), `{"name":"PLEXUS_TEST_VAR"}`); r.Content != "present" {
		t.Fatalf("get_env set = %q", r.Content)
	}
	if r := invoke(t, GetEnv(), `{"name":"PLEXUS_DEFINITELY_UNSET_XYZ"}`); !strings.Contains(r.Content, "not set") {
		t.Fatalf("get_env unset = %q", r.Content)
	}
	if r := invoke(t, Now(), `{}`); r.IsError || !strings.Contains(r.Content, "unix ") {
		t.Fatalf("now = %q", r.Content)
	}
	if r := invoke(t, GetCwd(), `{}`); r.IsError || r.Content == "" {
		t.Fatalf("get_cwd = %q", r.Content)
	}
}

func newWM(t *testing.T) *store.WorkingMemoryStore {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	wm, err := store.NewWorkingMemoryStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewWorkingMemoryStore: %v", err)
	}
	return wm
}

func TestMemoryEffectorsRoundTrip(t *testing.T) {
	wm := newWM(t)
	ctx := WithTaskScope(context.Background(), "task-7")
	write, read := MemWrite(wm), MemRead(wm)

	if _, err := write.Invoke(ctx, json.RawMessage(`{"key":"constraint","content":"no network"}`)); err != nil {
		t.Fatalf("mem_write: %v", err)
	}
	// read back by key
	r, _ := read.Invoke(ctx, json.RawMessage(`{"key":"constraint"}`))
	if r.IsError || r.Content != "no network" {
		t.Fatalf("mem_read key = %q isErr=%v", r.Content, r.IsError)
	}
	// list all (no key)
	r, _ = read.Invoke(ctx, json.RawMessage(`{}`))
	if !strings.Contains(r.Content, "constraint: no network") {
		t.Fatalf("mem_read list = %q", r.Content)
	}
	// scope isolation: a different scope sees nothing
	other := WithTaskScope(context.Background(), "task-other")
	r, _ = read.Invoke(other, json.RawMessage(`{"key":"constraint"}`))
	if !r.IsError {
		t.Fatalf("mem_read cross-scope should miss, got %q", r.Content)
	}
}

func TestLtmStubsReportNotImplemented(t *testing.T) {
	if r := invoke(t, LtmGet(), `{"query":"x"}`); !r.IsError || !strings.Contains(r.Content, "not implemented") {
		t.Fatalf("ltm_get = %q isErr=%v", r.Content, r.IsError)
	}
	if r := invoke(t, LtmPut(), `{"key":"k","content":"v"}`); !r.IsError {
		t.Fatalf("ltm_put: want IsError")
	}
}

// TestMemoryIsAgentPrivate verifies the §5.7.7 invariant: memory effectors are
// excluded from the delegation envelope even though they are approval-free,
// while ordinary file primitives stay inside it.
func TestMemoryIsAgentPrivate(t *testing.T) {
	wm := newWM(t)
	r := NewRegistry(nil)
	RegisterBuiltins(r, BuiltinOptions{WorkingMemory: wm})

	env := r.DelegationEnvelope()
	inEnvelope := map[string]bool{}
	for _, e := range env.List() {
		inEnvelope[e.Name()] = true
	}
	for _, private := range []string{"mem_read", "mem_write", "ltm_get", "ltm_put"} {
		if inEnvelope[private] {
			t.Errorf("agent-private %q leaked into delegation envelope", private)
		}
	}
	for _, shared := range []string{"read_file", "write_file", "edit_file", "search"} {
		if !inEnvelope[shared] {
			t.Errorf("shareable primitive %q missing from delegation envelope", shared)
		}
	}
	// run_command is opt-in and excluded by default.
	if _, ok := r.Get("run_command"); ok {
		t.Errorf("run_command should not be registered without IncludeRunCommand")
	}
}

func TestBuiltinsMemOmittedWithoutStore(t *testing.T) {
	r := NewRegistry(nil)
	RegisterBuiltins(r, BuiltinOptions{}) // no WorkingMemory
	if _, ok := r.Get("mem_read"); ok {
		t.Error("mem_read registered despite no WorkingMemory store")
	}
	// ltm stubs are always present.
	if _, ok := r.Get("ltm_get"); !ok {
		t.Error("ltm_get should be registered")
	}
}
