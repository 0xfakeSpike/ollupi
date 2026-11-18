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
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/jsonschema-go/jsonschema"
)

const (
	ToolReadFile = "read_file"
	ToolGlob     = "glob"
	ToolGrep     = "grep"

	maxReadLines    = 2000
	maxReadBytes    = 256 << 10
	maxReadScan     = 64 << 20
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

type ReadFileOutput struct {
	Path              string `json:"path"`
	Text              string `json:"text,omitempty"`
	StartLine         int    `json:"startLine,omitempty"`
	EndLine           int    `json:"endLine,omitempty"`
	TotalLines        int    `json:"totalLines"`
	TotalLinesAtLeast bool   `json:"totalLinesAtLeast,omitempty"`
	Binary            bool   `json:"binary,omitempty"`
	MimeType          string `json:"mimeType,omitempty"`
	Size              int64  `json:"size"`
}

type Glob struct {
	Pattern string `json:"pattern" jsonschema:"Path pattern relative to path: * matches within a name, ** any number of folders, {a,b} either; e.g. **/*.go or src/**/*_test.{ts,tsx}."`
	Path    string `json:"path,omitempty" jsonschema:"Folder to search, relative to the project root or absolute; default the project root."`
}

func (Glob) Constrain(s *jsonschema.Schema) {
	s.Properties["pattern"].MinLength = jsonschema.Ptr(1)
}

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

type GrepLine struct {
	Line  int    `json:"line"`
	Text  string `json:"text"`
	Match bool   `json:"match,omitempty"`
}

func (r *Provider) readTools() []kit.Spec {
	annotations := func(title string) tool.Annotations {
		return tool.Annotations{Title: title, ReadOnlyHint: true, DestructiveHint: new(false), OpenWorldHint: new(false)}
	}
	closed := " Credential files and the agent's own data are never read, even with the sandbox off."
	return []kit.Spec{{
		Name: ToolReadFile,
		Description: "Read a text file, its lines numbered. Reads up to 2000 lines from offset; for a longer file read the next window. " +
			"A file that is not text comes back as its type and size. Use this, not cat or sed in run_command, to read files." + closed,
		InputSchema: kit.InputSchema[ReadFile](),
		Annotations: annotations("Read file"),
		Content:     r.readContent,
	}, {
		Name: ToolGlob,
		Description: "Find files by path pattern, newest first, skipping .git and what .gitignore ignores. " +
			"Use this, not find or ls in run_command, to find files." + closed,
		InputSchema: kit.InputSchema[Glob](),
		Annotations: annotations("Find files"),
		Content:     r.readContent,
	}, {
		Name: ToolGrep,
		Description: "Search file contents with a regular expression, skipping .git, what .gitignore ignores, and files that are not text. " +
			"Returns matching lines with their line numbers, grouped by file, or with files_only just the files. " +
			"Use this, not grep or rg in run_command, to search." + closed,
		InputSchema: kit.InputSchema[Grep](),
		Annotations: annotations("Search files"),
		Content:     r.readContent,
	}}
}

func (r *Provider) readContent(ctx context.Context, c tool.Call) (policy.Content, error) {
	caller, args := c.Caller, c.Arguments
	d, err := r.dirs(ctx, caller)
	if err != nil {
		return policy.Content{}, err
	}
	raw, _ := args["path"].(string)
	abs, err := readable(ctx, d, raw)
	if err != nil {
		return policy.Content{}, err
	}
	shown := d.display(abs)
	text := shown
	if pattern, _ := args["pattern"].(string); pattern != "" {
		text = pattern + " in " + shown
	}
	return policy.Content{Text: text, Match: strings.ToLower(shown), Folded: true}, nil
}

func readLine(br *bufio.Reader, limit int) (string, int, error) {
	var kept []byte
	read := 0
	for {
		chunk, err := br.ReadSlice('\n')
		read += len(chunk)
		if room := limit - len(kept); room > 0 {
			kept = append(kept, chunk[:min(len(chunk), room)]...)
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return strings.ToValidUTF8(string(kept), ""), read, err
		}
	}
}

func readable(ctx context.Context, d dirs, raw string) (string, error) {
	abs, err := d.box.Canonical(ctx, d.abs(strings.TrimSpace(raw)))
	if err != nil {
		return "", err
	}
	if err := d.box.CheckRead(abs); err != nil {
		return "", fmt.Errorf("workspace: %s is closed to the read tools; if the task needs it, run a command so the user decides: %w", raw, err)
	}
	return abs, nil
}

func (d dirs) display(abs string) string {
	for _, root := range d.roots {
		if sandbox.Within(root, abs) {
			rel, _ := filepath.Rel(root, abs)
			return filepath.ToSlash(rel)
		}
	}
	return abs
}

func (r *Provider) invokeRead(ctx context.Context, caller tool.Caller, tool string, args map[string]any) (kit.Result, error) {
	d, err := r.dirs(ctx, caller)
	if err != nil {
		return kit.Result{}, err
	}
	var out any
	switch tool {
	case ToolReadFile:
		out, err = readFile(ctx, d, args)
	case ToolGlob:
		out, err = glob(ctx, d, args)
	case ToolGrep:
		out, err = grep(ctx, d, args)
	default:
		err = fmt.Errorf("workspace: unknown tool %q", tool)
	}
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: out}, nil
}

func readFile(ctx context.Context, d dirs, args map[string]any) (ReadFileOutput, error) {
	in, err := kit.DecodeArguments[ReadFile](args)
	if err != nil {
		return ReadFileOutput{}, err
	}
	abs, err := readable(ctx, d, in.Path)
	if err != nil {
		return ReadFileOutput{}, err
	}
	st, err := d.box.Stat(ctx, abs)
	if err != nil {
		return ReadFileOutput{}, fmt.Errorf("workspace: %w", err)
	}
	out := ReadFileOutput{Path: d.display(abs), Size: st.Size()}
	if st.IsDir() {
		return ReadFileOutput{}, fmt.Errorf("workspace: %s is a folder; list it with %s", out.Path, ToolGlob)
	}
	if !st.Mode().IsRegular() {
		return ReadFileOutput{}, fmt.Errorf("workspace: %s is not a regular file", out.Path)
	}
	f, err := d.box.Open(ctx, abs)
	if err != nil {
		return ReadFileOutput{}, fmt.Errorf("workspace: %w", err)
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, binarySniffSize)
	head, err := br.Peek(binarySniffSize)
	if err != nil && !errors.Is(err, io.EOF) {
		return ReadFileOutput{}, err
	}
	if !isText(head) {
		out.Binary = true
		out.MimeType, _, _ = strings.Cut(http.DetectContentType(head), ";")
		return out, nil
	}
	start, limit := max(in.Offset, 1), in.Limit
	if limit <= 0 {
		limit = maxReadLines
	}
	var text strings.Builder
	scanned := 0
	for {
		if err := ctx.Err(); err != nil {
			return ReadFileOutput{}, err
		}
		line, n, err := readLine(br, maxLineRunes*utf8.UTFMax)
		scanned += n
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
		if scanned > maxReadScan && (out.TotalLines >= start+limit-1 || text.Len() >= maxReadBytes) {
			out.TotalLinesAtLeast = true
			break
		}
	}
	if out.TotalLines > 0 && start > out.TotalLines {
		return ReadFileOutput{}, fmt.Errorf("workspace: %s has %d lines; offset %d is past its end", out.Path, out.TotalLines, start)
	}
	out.Text = text.String()
	return out, nil
}

func glob(ctx context.Context, d dirs, args map[string]any) (GlobOutput, error) {
	in, _ := kit.DecodeArguments[Glob](args)
	pattern, err := checkPattern(in.Pattern)
	if err != nil {
		return GlobOutput{}, fmt.Errorf("workspace: %w", err)
	}
	dir, err := readable(ctx, d, in.Path)
	if err != nil {
		return GlobOutput{}, err
	}
	type found struct {
		path string
		mod  time.Time
	}
	var all []found
	w := newWalker(ctx, d.box, dir)
	err = w.walk(ctx, func(abs string, info fs.FileInfo) error {
		rel, _ := filepath.Rel(dir, abs)
		if matched, _ := doublestar.Match(pattern, filepath.ToSlash(rel)); matched {
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

func grep(ctx context.Context, d dirs, args map[string]any) (GrepOutput, error) {
	in, _ := kit.DecodeArguments[Grep](args)
	expr := in.Pattern
	if in.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return GrepOutput{}, fmt.Errorf("workspace: pattern: %w", err)
	}
	include := ""
	if in.Include != "" {
		if include, err = checkPattern(in.Include); err != nil {
			return GrepOutput{}, fmt.Errorf("workspace: include: %w", err)
		}
	}
	path, err := readable(ctx, d, in.Path)
	if err != nil {
		return GrepOutput{}, err
	}
	out := GrepOutput{Path: d.display(path), Files: []GrepFile{}}
	around := in.Context
	shown, unreadable := 0, 0
	var readErr error
	search := func(abs string, info fs.FileInfo) error {
		if out.Truncated {
			return errStopWalk
		}
		if include != "" {
			rel, _ := filepath.Rel(path, abs)
			if matched, _ := doublestar.Match(include, filepath.ToSlash(rel)); !matched {
				return nil
			}
		}
		if info.Size() > maxGrepBytes {
			return nil
		}
		f, kept, err := grepFile(ctx, d.box, abs, re, around, in.FilesOnly, maxGrepLines-shown)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			unreadable, readErr = unreadable+1, err
			return nil
		}
		if f.Matches == 0 {
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
	st, err := d.box.Stat(ctx, path)
	if err != nil {
		return GrepOutput{}, fmt.Errorf("workspace: %w", err)
	}
	if !st.IsDir() {
		if !st.Mode().IsRegular() {
			return GrepOutput{}, fmt.Errorf("workspace: %s is not a regular file", d.display(path))
		}
		if err := search(path, st); err != nil {
			return GrepOutput{}, err
		}
		if readErr != nil {
			return GrepOutput{}, fmt.Errorf("workspace: %s: %w", out.Path, readErr)
		}
		return out, nil
	}
	w := newWalker(ctx, d.box, path)
	if err := w.walk(ctx, search); err != nil && !errors.Is(err, errStopWalk) {
		return GrepOutput{}, err
	}
	out.Skipped, out.Truncated = w.skipped+unreadable, out.Truncated || w.truncated
	return out, nil
}

func grepFile(ctx context.Context, box sandbox.Box, path string, re *regexp.Regexp, around int, filesOnly bool, room int) (GrepFile, int, error) {
	f, err := box.Open(ctx, path)
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
