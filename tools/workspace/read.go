package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

const (
	ToolReadFile = "read_file"
	ToolGlob     = "glob"
	ToolGrep     = "grep"

	maxReadLines    = 2000
	maxReadBytes    = 256 << 10
	maxLineRunes    = 2000
	maxGlobFiles    = 500
	maxGrepLines    = 300
	maxGrepFiles    = 500
	maxGrepContext  = 10
	maxGrepRunes    = 500
	maxGrepBytes    = 8 << 20
	maxWalkEntries  = 200_000
	binarySniffSize = 8000
)

type ReadFile struct {
	Path   string `json:"path" jsonschema:"File to read, relative to the project root or absolute."`
	Offset int    `json:"offset,omitempty" jsonschema:"First line to read, counting from 1; default 1."`
	Limit  int    `json:"limit,omitempty" jsonschema:"How many lines to read; default and at most 2000."`
}

func (ReadFile) Constrain(s *jsonschema.Schema) {
	s.Properties["path"].MinLength = jsonschema.Ptr(1)
	s.Properties["offset"].Minimum = jsonschema.Ptr(1.0)
	s.Properties["limit"].Minimum = jsonschema.Ptr(1.0)
	s.Properties["limit"].Maximum = jsonschema.Ptr(float64(maxReadLines))
}

// ReadFileOutput is a window of a text file, its lines numbered, or what a file that is not text is.
type ReadFileOutput struct {
	Path       string `json:"path"`
	Text       string `json:"text,omitempty"`
	StartLine  int    `json:"startLine,omitempty"`
	EndLine    int    `json:"endLine,omitempty"`
	TotalLines int    `json:"totalLines"`
	Binary     bool   `json:"binary,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	Size       int64  `json:"size"`
}

type Glob struct {
	Pattern string `json:"pattern" jsonschema:"Path pattern relative to path: * matches within a name, ** any number of folders, {a,b} either; e.g. **/*.go or src/**/*_test.{ts,tsx}."`
	Path    string `json:"path,omitempty" jsonschema:"Folder to search, relative to the project root or absolute; default the project root."`
}

func (Glob) Constrain(s *jsonschema.Schema) {
	s.Properties["pattern"].MinLength = jsonschema.Ptr(1)
}

// GlobOutput lists the matching files, newest first, as read_file takes them.
type GlobOutput struct {
	Path      string   `json:"path"`
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated,omitempty"`
	Skipped   int      `json:"skipped,omitempty"`
}

type Grep struct {
	Pattern    string `json:"pattern" jsonschema:"Regular expression to search for (Go RE2 syntax), matched against each line."`
	Path       string `json:"path,omitempty" jsonschema:"File or folder to search, relative to the project root or absolute; default the project root."`
	Include    string `json:"include,omitempty" jsonschema:"Only files whose path relative to path matches this pattern, e.g. **/*.go or *.{ts,tsx}."`
	IgnoreCase bool   `json:"ignore_case,omitempty" jsonschema:"Match letters in either case."`
	FilesOnly  bool   `json:"files_only,omitempty" jsonschema:"Return only the files that match, not their lines."`
	Context    int    `json:"context,omitempty" jsonschema:"Lines to show before and after each matching line, at most 10."`
}

func (Grep) Constrain(s *jsonschema.Schema) {
	s.Properties["pattern"].MinLength = jsonschema.Ptr(1)
	s.Properties["context"].Minimum = jsonschema.Ptr(0.0)
	s.Properties["context"].Maximum = jsonschema.Ptr(float64(maxGrepContext))
}

// GrepOutput lists the files with matching lines, in path order.
type GrepOutput struct {
	Path      string     `json:"path"`
	Files     []GrepFile `json:"files"`
	Matches   int        `json:"matches"`
	Truncated bool       `json:"truncated,omitempty"`
	Skipped   int        `json:"skipped,omitempty"`
}

type GrepFile struct {
	Path    string     `json:"path"`
	Matches int        `json:"matches"`
	Lines   []GrepLine `json:"lines,omitempty"`
}

// GrepLine is a matching line, or a line of context around one.
type GrepLine struct {
	Line  int    `json:"line"`
	Text  string `json:"text"`
	Match bool   `json:"match,omitempty"`
}

func (r *Provider) readTools() []*tools.Spec {
	annotations := func(title string) *tools.Annotations {
		return &tools.Annotations{Title: title, ReadOnlyHint: true, DestructiveHint: tools.Hint(false), OpenWorldHint: tools.Hint(false)}
	}
	closed := " Credential files and the agent's own data are never read, even with the sandbox off."
	return []*tools.Spec{{
		Name: ToolReadFile,
		Description: "Read a text file, its lines numbered. Reads up to 2000 lines from offset; for a longer file read the next window. " +
			"A file that is not text comes back as its type and size. Use this, not cat or sed in run_command, to read files." + closed,
		InputSchema:       tools.InputSchema[ReadFile](),
		Annotations:       annotations("Read file"),
		PermissionContent: r.readContent,
	}, {
		Name: ToolGlob,
		Description: "Find files by path pattern, newest first, skipping .git and what .gitignore ignores. " +
			"Use this, not find or ls in run_command, to find files." + closed,
		InputSchema:       tools.InputSchema[Glob](),
		Annotations:       annotations("Find files"),
		PermissionContent: r.readContent,
	}, {
		Name: ToolGrep,
		Description: "Search file contents with a regular expression, skipping .git, what .gitignore ignores, and files that are not text. " +
			"Returns matching lines with their line numbers, grouped by file, or with files_only just the files. " +
			"Use this, not grep or rg in run_command, to search." + closed,
		InputSchema:       tools.InputSchema[Grep](),
		Annotations:       annotations("Search files"),
		PermissionContent: r.readContent,
	}}
}

// readContent is what permission rules match: the file or folder read, relative to the project root inside it.
func (r *Provider) readContent(ctx context.Context, args map[string]any) (tools.Content, error) {
	d, err := r.dirs(tools.CallerOf(ctx))
	if err != nil {
		return tools.Content{}, err
	}
	raw, _ := args["path"].(string)
	abs, err := r.readable(d, raw)
	if err != nil {
		return tools.Content{}, err
	}
	shown := d.display(abs)
	text := shown
	if pattern, _ := args["pattern"].(string); pattern != "" {
		text = pattern + " in " + shown
	}
	return tools.Content{Text: text, Match: strings.ToLower(shown)}, nil
}

// readable resolves a path the read tools take, following links, and checks that the sandbox lets it be read.
func (r *Provider) readable(d dirs, raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		path = d.roots[0]
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(d.roots[0], path)
	}
	abs, err := sandbox.CanonicalPath(path, "")
	if err != nil {
		return "", err
	}
	if err := r.box.CheckRead(abs); err != nil {
		return "", fmt.Errorf("workspace: %s is closed to the read tools; if the task needs it, run a command so the user decides: %w", raw, err)
	}
	return abs, nil
}

// display shows a path as the tools take it: relative to the root it is in, or absolute outside every root.
func (d dirs) display(abs string) string {
	for _, root := range d.roots {
		if sandbox.Within(root, abs) {
			rel, _ := filepath.Rel(root, abs)
			return filepath.ToSlash(rel)
		}
	}
	return abs
}

func (r *Provider) invokeRead(ctx context.Context, tool string, args map[string]any) (tools.Result, error) {
	d, err := r.dirs(tools.CallerOf(ctx))
	if err != nil {
		return tools.Result{}, err
	}
	var out any
	switch tool {
	case ToolReadFile:
		out, err = r.readFile(d, args)
	case ToolGlob:
		out, err = r.glob(ctx, d, args)
	case ToolGrep:
		out, err = r.grep(ctx, d, args)
	default:
		err = fmt.Errorf("workspace: unknown tool %q", tool)
	}
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Output: out}, nil
}

func (r *Provider) readFile(d dirs, args map[string]any) (ReadFileOutput, error) {
	in, err := tools.DecodeArguments[ReadFile](args)
	if err != nil {
		return ReadFileOutput{}, err
	}
	abs, err := r.readable(d, in.Path)
	if err != nil {
		return ReadFileOutput{}, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return ReadFileOutput{}, fmt.Errorf("workspace: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ReadFileOutput{}, err
	}
	out := ReadFileOutput{Path: d.display(abs), Size: st.Size()}
	if st.IsDir() {
		return ReadFileOutput{}, fmt.Errorf("workspace: %s is a folder; list it with %s", out.Path, ToolGlob)
	}
	if !st.Mode().IsRegular() {
		return ReadFileOutput{}, fmt.Errorf("workspace: %s is not a regular file", out.Path)
	}
	head := make([]byte, binarySniffSize)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return ReadFileOutput{}, err
	}
	if !isText(head[:n]) {
		out.Binary = true
		out.MimeType, _, _ = strings.Cut(http.DetectContentType(head[:n]), ";")
		return out, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ReadFileOutput{}, err
	}
	start, limit := max(in.Offset, 1), in.Limit
	if limit <= 0 || limit > maxReadLines {
		limit = maxReadLines
	}
	var text strings.Builder
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			out.TotalLines++
			if n := out.TotalLines; n >= start && n < start+limit && text.Len() < maxReadBytes {
				fmt.Fprintf(&text, "%d\t%s\n", n, cut(strings.TrimRight(line, "\r\n"), maxLineRunes))
				out.StartLine, out.EndLine = start, n
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ReadFileOutput{}, err
		}
	}
	if out.TotalLines > 0 && start > out.TotalLines {
		return ReadFileOutput{}, fmt.Errorf("workspace: %s has %d lines; offset %d is past its end", out.Path, out.TotalLines, start)
	}
	out.Text = text.String()
	return out, nil
}

func (r *Provider) glob(ctx context.Context, d dirs, args map[string]any) (GlobOutput, error) {
	in, err := tools.DecodeArguments[Glob](args)
	if err != nil {
		return GlobOutput{}, err
	}
	re, err := compilePattern(strings.TrimPrefix(in.Pattern, "./"), true)
	if err != nil {
		return GlobOutput{}, fmt.Errorf("workspace: %w", err)
	}
	dir, err := r.readable(d, in.Path)
	if err != nil {
		return GlobOutput{}, err
	}
	type found struct {
		path string
		mod  time.Time
	}
	var all []found
	w := r.walker(dir)
	err = w.walk(ctx, func(abs string, info fs.FileInfo) error {
		rel, _ := filepath.Rel(dir, abs)
		if re.MatchString(filepath.ToSlash(rel)) {
			all = append(all, found{d.display(abs), info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return GlobOutput{}, err
	}
	slices.SortStableFunc(all, func(a, b found) int { return b.mod.Compare(a.mod) })
	out := GlobOutput{Path: d.display(dir), Files: []string{}, Skipped: w.skipped, Truncated: w.truncated || len(all) > maxGlobFiles}
	for _, f := range all[:min(len(all), maxGlobFiles)] {
		out.Files = append(out.Files, f.path)
	}
	return out, nil
}

func (r *Provider) grep(ctx context.Context, d dirs, args map[string]any) (GrepOutput, error) {
	in, err := tools.DecodeArguments[Grep](args)
	if err != nil {
		return GrepOutput{}, err
	}
	expr := in.Pattern
	if in.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return GrepOutput{}, fmt.Errorf("workspace: pattern: %w", err)
	}
	var include *regexp.Regexp
	if in.Include != "" {
		if include, err = compilePattern(strings.TrimPrefix(in.Include, "./"), true); err != nil {
			return GrepOutput{}, fmt.Errorf("workspace: include: %w", err)
		}
	}
	path, err := r.readable(d, in.Path)
	if err != nil {
		return GrepOutput{}, err
	}
	out := GrepOutput{Path: d.display(path), Files: []GrepFile{}}
	around := min(max(in.Context, 0), maxGrepContext)
	shown := 0
	search := func(abs string, info fs.FileInfo) error {
		if out.Truncated {
			return errStopWalk
		}
		if include != nil {
			rel, _ := filepath.Rel(path, abs)
			if !include.MatchString(filepath.ToSlash(rel)) {
				return nil
			}
		}
		if info.Size() > maxGrepBytes {
			return nil
		}
		f, kept, err := grepFile(abs, re, around, in.FilesOnly, maxGrepLines-shown)
		if err != nil || f.Matches == 0 {
			return nil
		}
		f.Path = d.display(abs)
		out.Files = append(out.Files, f)
		out.Matches += f.Matches
		shown += kept
		if (in.FilesOnly && len(out.Files) >= maxGrepFiles) || (!in.FilesOnly && shown >= maxGrepLines) {
			out.Truncated = true
		}
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return GrepOutput{}, fmt.Errorf("workspace: %w", err)
	}
	if !st.IsDir() {
		err = search(path, st)
		return out, err
	}
	w := r.walker(path)
	if err := w.walk(ctx, search); err != nil && !errors.Is(err, errStopWalk) {
		return GrepOutput{}, err
	}
	out.Skipped, out.Truncated = w.skipped, out.Truncated || w.truncated
	return out, nil
}

// grepFile searches one text file. It reports the file with how many of its lines match and, unless
// filesOnly, up to room of those lines with their context, and how many matching lines it kept. A file that
// is not text matches nothing.
func grepFile(path string, re *regexp.Regexp, around int, filesOnly bool, room int) (GrepFile, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return GrepFile{}, 0, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	if head, _ := br.Peek(binarySniffSize); !isText(head) {
		return GrepFile{}, 0, nil
	}
	var (
		out     GrepFile
		before  []GrepLine
		after   int
		printed int
		kept    int
	)
	for n := 1; ; n++ {
		line, err := br.ReadString('\n')
		if line == "" && err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return GrepFile{}, 0, err
		}
		text := strings.TrimRight(line, "\r\n")
		match := re.MatchString(text)
		if match {
			out.Matches++
		}
		if !filesOnly {
			g := GrepLine{Line: n, Text: cut(text, maxGrepRunes), Match: match}
			switch {
			case match && kept < room:
				for _, b := range before {
					if b.Line > printed {
						out.Lines = append(out.Lines, b)
					}
				}
				out.Lines = append(out.Lines, g)
				printed, after, kept, before = n, around, kept+1, nil
			case after > 0:
				out.Lines = append(out.Lines, g)
				printed, after = n, after-1
			case around > 0:
				before = append(before, g)
				if len(before) > around {
					before = before[1:]
				}
			}
		}
		if err != nil {
			break
		}
	}
	return out, kept, nil
}

var errStopWalk = errors.New("stop walk")

// walker visits the files under a folder in name order. It skips .git, what .gitignore ignores, links to
// folders, and anything the sandbox does not let be read, counting those; a link to a file is read where it
// leads, so a link cannot reach a closed path.
type walker struct {
	box       *sandbox.Sandbox
	root      string
	ignores   *ignores
	entries   int
	skipped   int
	truncated bool
}

func (r *Provider) walker(root string) *walker {
	return &walker{box: r.box, root: root, ignores: gitIgnores(root)}
}

func (w *walker) walk(ctx context.Context, visit func(abs string, info fs.FileInfo) error) error {
	st, err := os.Stat(w.root)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("workspace: %s is not a folder", w.root)
	}
	return w.dir(ctx, w.root, visit)
}

func (w *walker) dir(ctx context.Context, dir string, visit func(string, fs.FileInfo) error) error {
	if w.ignores != nil && w.ignores.enter(dir) {
		defer w.ignores.leave()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		w.skipped++
		return nil
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if w.entries++; w.entries > maxWalkEntries {
			w.truncated = true
			return errStopWalk
		}
		if e.Name() == ".git" {
			continue
		}
		abs := filepath.Join(dir, e.Name())
		if w.box.CheckRead(abs) != nil {
			w.skipped++
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		isDir := info.IsDir()
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(abs)
			if err != nil {
				continue
			}
			if w.box.CheckRead(target) != nil {
				w.skipped++
				continue
			}
			if info, err = os.Stat(target); err != nil || info.IsDir() {
				continue
			}
		}
		if w.ignores.ignored(abs, isDir) {
			continue
		}
		if isDir {
			err = w.dir(ctx, abs, visit)
		} else if info.Mode().IsRegular() {
			err = visit(abs, info)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// isText reports whether the start of a file reads as text: no NUL bytes and valid UTF-8, allowing a
// character cut off at the end.
func isText(head []byte) bool {
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	for i := 0; i < utf8.UTFMax && len(head) > 0 && !utf8.Valid(head); i++ {
		head = head[:len(head)-1]
	}
	return utf8.Valid(head)
}

func cut(s string, runes int) string {
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	return string([]rune(s)[:runes]) + "…"
}
