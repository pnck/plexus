package effector

import (
	"bytes"
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
//
// Positions are BYTE OFFSETS, the one currency shared across the read/find/edit
// trio: search reports each match's offset, read_file slices a [offset, length)
// range, and edit_file confines its replace to a [start, end) range. So the agent
// can locate a string (search → offsets) and act on one specific occurrence
// (read or edit that range) instead of disambiguating by surrounding context.

// pathArg is the lone-path argument shared by the path-only effectors.
type pathArg struct {
	Path string `json:"path" desc:"Filesystem path."`
}

type readFileArgs struct {
	Path   string `json:"path" desc:"Filesystem path."`
	Offset int    `json:"offset,omitempty" desc:"Start byte offset (default 0). Pairs with search offsets / stat size."`
	Length int    `json:"length,omitempty" desc:"Bytes to read from offset (0 or omitted = to end of file)."`
}

// ReadFile returns the built-in read_file effector (RiskTag Read). With no
// offset/length it returns the whole file; with them it returns the byte slice
// [offset, offset+length), clamped to the file's bounds.
func ReadFile() Effector {
	return define(spec{
		Name: "read_file",
		Desc: "Read a file. Optionally read just a byte range via offset/length (e.g. an offset from search) instead of the whole file.",
		Risk: Read,
	}, func(_ context.Context, in readFileArgs) (Result, error) {
		if in.Path == "" {
			return toolErr("missing required argument: path"), nil
		}
		data, err := os.ReadFile(in.Path)
		if err != nil {
			return toolErr("read_file failed: %v", err), nil
		}
		if in.Offset == 0 && in.Length == 0 {
			return Result{Content: string(data)}, nil // whole file
		}
		start := clamp(in.Offset, 0, len(data))
		end := len(data)
		if in.Length > 0 {
			end = clamp(start+in.Length, start, len(data))
		}
		return Result{Content: string(data[start:end])}, nil
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
// search across files, built in so it needs no shell (and thus no approval). It
// reports EVERY match (not just one per line) with its byte offset, so the agent
// can read/edit that exact location.
func Search() Effector {
	return define(spec{
		Name: "search",
		Desc: "Search file contents by regular expression under a path. Reports each match as `path:line:@offset: text`; the @offset byte position feeds read_file (offset/length) and edit_file (start/end).",
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

// searchFile appends up to budget "path:line:@offset: text" matches from one
// file and returns how many it wrote. Every match (not just the first per line)
// is reported with its byte offset. Binary (non-UTF-8) files are skipped.
func searchFile(re *regexp.Regexp, path string, b *strings.Builder, budget int) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	if !utf8.Valid(data) {
		return 0 // looks binary
	}
	locs := re.FindAllIndex(data, budget)
	for _, loc := range locs {
		off := loc[0]
		line := 1 + bytes.Count(data[:off], []byte{'\n'})
		fmt.Fprintf(b, "%s:%d:@%d: %s\n", path, line, off, lineAround(data, off))
	}
	return len(locs)
}

// lineAround returns the text of the line containing byte offset off, with any
// trailing carriage return stripped.
func lineAround(data []byte, off int) string {
	start := bytes.LastIndexByte(data[:off], '\n') + 1 // 0 if no newline before
	end := len(data)
	if i := bytes.IndexByte(data[off:], '\n'); i >= 0 {
		end = off + i
	}
	return strings.TrimRight(string(data[start:end]), "\r")
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
	ReplaceAll bool   `json:"replace_all,omitempty" desc:"Replace every occurrence within the range (default false)."`
	Start      int    `json:"start,omitempty" desc:"Confine the replace to bytes [start, end) — e.g. to target one of several occurrences using a search offset. Default 0 (file start)."`
	End        int    `json:"end,omitempty" desc:"End byte offset of the confined range (0 or omitted = end of file)."`
}

// EditFile returns the built-in edit_file effector (RiskTag Write): exact string
// replacement (old -> new), Claude-Code style — a uniqueness check unless
// replace_all is set. The optional start/end byte range confines both the
// uniqueness check and the replacement to that region, so the agent can target
// one of several occurrences (located via search offsets) without expanding
// old_string with surrounding context.
func EditFile() Effector {
	return define(spec{
		Name: "edit_file",
		Desc: "Replace an exact string in a file. old_string must be unique (or set replace_all). Optionally confine to a byte range [start, end) — e.g. a search offset — to target one specific occurrence.",
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
		// Confine to [start, end) — default the whole file.
		start := clamp(in.Start, 0, len(content))
		end := len(content)
		if in.End > 0 {
			end = clamp(in.End, start, len(content))
		}
		if start > end {
			return toolErr("invalid range: start (%d) > end (%d)", start, end), nil
		}
		ranged := in.Start != 0 || in.End != 0
		region := content[start:end]

		n := strings.Count(region, in.OldString)
		switch {
		case n == 0:
			return toolErr("old_string not found in %s%s", in.Path, rangeSuffix(ranged, start, end)), nil
		case n > 1 && !in.ReplaceAll:
			return toolErr("old_string is not unique in %s%s (%d matches); narrow the range, add context, or set replace_all", in.Path, rangeSuffix(ranged, start, end), n), nil
		}
		var newRegion string
		if in.ReplaceAll {
			newRegion = strings.ReplaceAll(region, in.OldString, in.NewString)
		} else {
			newRegion = strings.Replace(region, in.OldString, in.NewString, 1)
		}
		updated := content[:start] + newRegion + content[end:]
		if err := os.WriteFile(in.Path, []byte(updated), info.Mode().Perm()); err != nil {
			return toolErr("edit_file failed: %v", err), nil
		}
		return Result{Content: fmt.Sprintf("edited %s (%d replacement(s))", in.Path, n)}, nil
	})
}

// rangeSuffix renders " in [start,end)" when a range was supplied, else "".
func rangeSuffix(ranged bool, start, end int) string {
	if !ranged {
		return ""
	}
	return fmt.Sprintf(" in [%d,%d)", start, end)
}

// clamp returns v constrained to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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
