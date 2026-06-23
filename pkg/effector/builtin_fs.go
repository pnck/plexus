package effector

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Built-in filesystem primitives (E2.7). All are Read or Write — never Exec — so
// they are auto-allowed and stay inside the delegation envelope: an agent (and a
// delegation) reads, writes, edits and searches files without an approval gate
// and without spawning a shell. Process execution lives in builtin_exec.go.

// pathArg is the lone-path argument shared by the path-only effectors.
type pathArg struct {
	Path string `json:"path" desc:"Filesystem path."`
}

// ReadFile returns the built-in read_file effector (RiskTag Read). No side
// effects, so it is auto-allowed and inside the delegation envelope.
func ReadFile() Effector {
	return define(spec{
		Name: "read_file",
		Desc: "Read the contents of a file at the given path.",
		Risk: Read,
	}, func(_ context.Context, in pathArg) (Result, error) {
		if in.Path == "" {
			return toolErr("missing required argument: path"), nil
		}
		data, err := os.ReadFile(in.Path)
		if err != nil {
			return toolErr("read_file failed: %v", err), nil
		}
		return Result{Content: string(data)}, nil
	})
}

// Stat returns the built-in stat effector (RiskTag Read).
func Stat() Effector {
	return define(spec{
		Name: "stat",
		Desc: "Report a path's size, mode, mtime and type; reports exists=false instead of erroring when absent.",
		Risk: Read,
	}, func(_ context.Context, in pathArg) (Result, error) {
		if in.Path == "" {
			return toolErr("missing required argument: path"), nil
		}
		info, err := os.Stat(in.Path)
		if os.IsNotExist(err) {
			return Result{Content: fmt.Sprintf(`{"path":%q,"exists":false}`, in.Path)}, nil
		}
		if err != nil {
			return toolErr("stat failed: %v", err), nil
		}
		return Result{Content: fmt.Sprintf(`{"path":%q,"exists":true,"is_dir":%t,"size":%d,"mode":%q,"mtime":%q}`,
			in.Path, info.IsDir(), info.Size(), info.Mode().String(), info.ModTime().UTC().Format("2006-01-02T15:04:05Z"))}, nil
	})
}

// ListDir returns the built-in list_dir effector (RiskTag Read).
func ListDir() Effector {
	return define(spec{
		Name: "list_dir",
		Desc: "List the entries of a directory (name, type, size).",
		Risk: Read,
	}, func(_ context.Context, in pathArg) (Result, error) {
		if in.Path == "" {
			return toolErr("missing required argument: path"), nil
		}
		entries, err := os.ReadDir(in.Path)
		if err != nil {
			return toolErr("list_dir failed: %v", err), nil
		}
		var b strings.Builder
		for _, e := range entries {
			if e.IsDir() {
				fmt.Fprintf(&b, "dir   %s/\n", e.Name())
				continue
			}
			size := int64(-1)
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
			fmt.Fprintf(&b, "file  %s (%d bytes)\n", e.Name(), size)
		}
		if b.Len() == 0 {
			return Result{Content: "(empty directory)"}, nil
		}
		return Result{Content: b.String()}, nil
	})
}

type globArgs struct {
	Pattern string `json:"pattern" desc:"Glob pattern."`
}

// Glob returns the built-in glob effector (RiskTag Read).
func Glob() Effector {
	return define(spec{
		Name: "glob",
		Desc: "Find files matching a shell glob pattern via path/filepath.",
		Risk: Read,
	}, func(_ context.Context, in globArgs) (Result, error) {
		if in.Pattern == "" {
			return toolErr("missing required argument: pattern"), nil
		}
		matches, err := filepath.Glob(in.Pattern)
		if err != nil {
			return toolErr("glob failed: %v", err), nil
		}
		if len(matches) == 0 {
			return Result{Content: "(no matches)"}, nil
		}
		return Result{Content: strings.Join(matches, "\n")}, nil
	})
}

const (
	searchMaxMatches  = 200
	searchMaxFileSize = 5 << 20 // 5 MiB — skip larger files
)

type searchArgs struct {
	Pattern string `json:"pattern" desc:"RE2 regular expression."`
	Path    string `json:"path,omitempty" desc:"File or directory root to search (default \".\")."`
}

// Search returns the built-in search effector (RiskTag Read): a regexp content
// search across files, built in so it needs no shell (and thus no approval).
func Search() Effector {
	return define(spec{
		Name: "search",
		Desc: "Search file contents by regular expression under a path; returns path:line: text matches (capped).",
		Risk: Read,
	}, func(_ context.Context, in searchArgs) (Result, error) {
		if in.Pattern == "" {
			return toolErr("missing required argument: pattern"), nil
		}
		re, err := regexp.Compile(in.Pattern)
		if err != nil {
			return toolErr("invalid regular expression: %v", err), nil
		}
		root := in.Path
		if root == "" {
			root = "."
		}
		var b strings.Builder
		matches := 0
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries rather than aborting the whole search
			}
			if d.IsDir() {
				return nil
			}
			if matches >= searchMaxMatches {
				return filepath.SkipAll
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > searchMaxFileSize {
				return nil
			}
			matches += searchFile(re, path, &b, searchMaxMatches-matches)
			return nil
		})
		if walkErr != nil {
			return toolErr("search failed: %v", walkErr), nil
		}
		if matches == 0 {
			return Result{Content: "(no matches)"}, nil
		}
		if matches >= searchMaxMatches {
			fmt.Fprintf(&b, "... (truncated at %d matches)\n", searchMaxMatches)
		}
		return Result{Content: b.String()}, nil
	})
}

// searchFile appends up to budget "path:line: text" matches from one file and
// returns how many it wrote. Binary (non-UTF-8) files are skipped.
func searchFile(re *regexp.Regexp, path string, b *strings.Builder, budget int) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	written, line := 0, 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if line == 1 && !utf8.ValidString(text) {
			return 0 // looks binary
		}
		if written >= budget {
			break
		}
		if re.MatchString(text) {
			fmt.Fprintf(b, "%s:%d: %s\n", path, line, text)
			written++
		}
	}
	return written
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode,omitempty" desc:"overwrite (default) replaces; append adds to the end." enum:"overwrite,append"`
}

// WriteFile returns the built-in write_file effector (RiskTag Write).
func WriteFile() Effector {
	return define(spec{
		Name: "write_file",
		Desc: "Write content to a regular file (mode overwrite|append).",
		Risk: Write,
	}, func(_ context.Context, in writeFileArgs) (Result, error) {
		if in.Path == "" {
			return toolErr("missing required argument: path"), nil
		}
		// Refuse to clobber anything that is not a regular file (dir, socket, pipe).
		if info, err := os.Lstat(in.Path); err == nil && !info.Mode().IsRegular() {
			return toolErr("write_file refuses non-regular file: %s (%s)", in.Path, info.Mode()), nil
		}
		flag := os.O_CREATE | os.O_WRONLY
		switch in.Mode {
		case "", "overwrite":
			flag |= os.O_TRUNC
		case "append":
			flag |= os.O_APPEND
		default:
			return toolErr("invalid mode %q (want overwrite|append)", in.Mode), nil
		}
		f, err := os.OpenFile(in.Path, flag, 0o644)
		if err != nil {
			return toolErr("write_file failed: %v", err), nil
		}
		if _, err := f.WriteString(in.Content); err != nil {
			_ = f.Close()
			return toolErr("write_file failed: %v", err), nil
		}
		if err := f.Close(); err != nil {
			return toolErr("write_file failed: %v", err), nil
		}
		mode := in.Mode
		if mode == "" {
			mode = "overwrite"
		}
		return Result{Content: fmt.Sprintf("wrote %d bytes to %s (%s)", len(in.Content), in.Path, mode)}, nil
	})
}

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty" desc:"Replace every occurrence (default false)."`
}

// EditFile returns the built-in edit_file effector (RiskTag Write): exact string
// replacement (old -> new), Claude-Code style — a uniqueness check unless
// replace_all is set. It is NOT line/offset based.
func EditFile() Effector {
	return define(spec{
		Name: "edit_file",
		Desc: "Replace an exact string in a file. old_string must be unique unless replace_all is true.",
		Risk: Write,
	}, func(_ context.Context, in editFileArgs) (Result, error) {
		if in.Path == "" {
			return toolErr("missing required argument: path"), nil
		}
		if in.OldString == in.NewString {
			return toolErr("old_string and new_string are identical; nothing to change"), nil
		}
		info, err := os.Stat(in.Path)
		if err != nil {
			return toolErr("edit_file failed: %v", err), nil
		}
		data, err := os.ReadFile(in.Path)
		if err != nil {
			return toolErr("edit_file failed: %v", err), nil
		}
		content := string(data)
		n := strings.Count(content, in.OldString)
		switch {
		case n == 0:
			return toolErr("old_string not found in %s", in.Path), nil
		case n > 1 && !in.ReplaceAll:
			return toolErr("old_string is not unique in %s (%d matches); add surrounding context or set replace_all", in.Path, n), nil
		}
		var updated string
		if in.ReplaceAll {
			updated = strings.ReplaceAll(content, in.OldString, in.NewString)
		} else {
			updated = strings.Replace(content, in.OldString, in.NewString, 1)
		}
		if err := os.WriteFile(in.Path, []byte(updated), info.Mode().Perm()); err != nil {
			return toolErr("edit_file failed: %v", err), nil
		}
		return Result{Content: fmt.Sprintf("edited %s (%d replacement(s))", in.Path, n)}, nil
	})
}

// MakeDir returns the built-in make_dir effector (RiskTag Write).
func MakeDir() Effector {
	return define(spec{
		Name: "make_dir",
		Desc: "Create a directory and any missing parents (mkdir -p).",
		Risk: Write,
	}, func(_ context.Context, in pathArg) (Result, error) {
		if in.Path == "" {
			return toolErr("missing required argument: path"), nil
		}
		if err := os.MkdirAll(in.Path, 0o755); err != nil {
			return toolErr("make_dir failed: %v", err), nil
		}
		return Result{Content: fmt.Sprintf("created %s", in.Path)}, nil
	})
}

type moveFileArgs struct {
	Source string `json:"source"`
	Dest   string `json:"dest"`
}

// MoveFile returns the built-in move_file effector (RiskTag Write).
func MoveFile() Effector {
	return define(spec{
		Name: "move_file",
		Desc: "Rename or move a file or directory from source to dest.",
		Risk: Write,
	}, func(_ context.Context, in moveFileArgs) (Result, error) {
		if in.Source == "" || in.Dest == "" {
			return toolErr("move_file requires source and dest"), nil
		}
		if err := os.Rename(in.Source, in.Dest); err != nil {
			return toolErr("move_file failed: %v", err), nil
		}
		return Result{Content: fmt.Sprintf("moved %s -> %s", in.Source, in.Dest)}, nil
	})
}

// RemoveFile returns the built-in remove_file effector (RiskTag Write). It
// removes a regular file only — refusing directories — and is reversible via
// VCS, so it is approval-free.
func RemoveFile() Effector {
	return define(spec{
		Name: "remove_file",
		Desc: "Delete a regular file (refuses directories).",
		Risk: Write,
	}, func(_ context.Context, in pathArg) (Result, error) {
		if in.Path == "" {
			return toolErr("missing required argument: path"), nil
		}
		info, err := os.Lstat(in.Path)
		if err != nil {
			return toolErr("remove_file failed: %v", err), nil
		}
		if info.IsDir() {
			return toolErr("remove_file refuses to delete a directory: %s", in.Path), nil
		}
		if err := os.Remove(in.Path); err != nil {
			return toolErr("remove_file failed: %v", err), nil
		}
		return Result{Content: fmt.Sprintf("removed %s", in.Path)}, nil
	})
}
