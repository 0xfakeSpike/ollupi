package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// compilePattern turns a path pattern into a regexp over slash-separated relative paths: * matches within
// one name, ** any number of folders, ? one character, [...] a character class ([!...] negates), \ quotes
// the next character, and with braces {a,b} either alternative.
func compilePattern(p string, braces bool) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	depth := 0
	for i := 0; i < len(p); {
		c := p[i]
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i += 2
		case c == '*':
			b.WriteString("[^/]*")
			i++
		case c == '?':
			b.WriteString("[^/]")
			i++
		case c == '[':
			end := strings.IndexByte(p[i+1:], ']')
			if end < 0 {
				return nil, fmt.Errorf("pattern %q: [ without ]", p)
			}
			class := p[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 2
		case c == '\\' && i+1 < len(p):
			b.WriteString(regexp.QuoteMeta(p[i+1 : i+2]))
			i += 2
		case braces && c == '{':
			b.WriteString("(?:")
			depth++
			i++
		case braces && c == ',' && depth > 0:
			b.WriteString("|")
			i++
		case braces && c == '}' && depth > 0:
			b.WriteString(")")
			depth--
			i++
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
			i++
		}
	}
	if depth > 0 {
		return nil, fmt.Errorf("pattern %q: { without }", p)
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", p, err)
	}
	return re, nil
}

// ignoreRule is one line of a .gitignore file.
type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// ignoreFile holds the rules of one .gitignore file; base is its folder relative to the work tree, "" at the top.
type ignoreFile struct {
	base  string
	rules []ignoreRule
}

// parseIgnore reads gitignore lines: comments, ! to re-include, a trailing / for folders only, and a pattern
// with a slash before its end anchored to base; one without matches a name at any depth.
func parseIgnore(base, text string) ignoreFile {
	f := ignoreFile{base: base}
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(strings.TrimSuffix(line, "\r"), " ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var r ignoreRule
		if strings.HasPrefix(line, "!") {
			r.negate, line = true, line[1:]
		} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		if !strings.Contains(line, "/") {
			line = "**/" + line
		}
		re, err := compilePattern(strings.TrimPrefix(line, "/"), false)
		if err != nil {
			continue
		}
		r.re = re
		f.rules = append(f.rules, r)
	}
	return f
}

// ignores is what a walk skips under the .gitignore files of the work tree it is in.
type ignores struct {
	top   string
	files []ignoreFile
}

// gitIgnores finds the work tree dir is in and reads .git/info/exclude and the .gitignore files from the top
// down to dir's parent; dir's own is read when the walk enters it. Outside a work tree nothing is ignored.
func gitIgnores(dir string) *ignores {
	top := dir
	for {
		if _, err := os.Stat(filepath.Join(top, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(top)
		if parent == top {
			return nil
		}
		top = parent
	}
	g := &ignores{top: top}
	if body, err := os.ReadFile(filepath.Join(top, ".git", "info", "exclude")); err == nil {
		g.files = append(g.files, parseIgnore("", string(body)))
	}
	for at := top; at != dir; {
		g.enter(at)
		rel, _ := filepath.Rel(at, dir)
		at = filepath.Join(at, strings.SplitN(rel, string(filepath.Separator), 2)[0])
	}
	return g
}

// enter reads dir's .gitignore, if it has one, and reports whether it did; leave undoes it.
func (g *ignores) enter(dir string) bool {
	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return false
	}
	g.files = append(g.files, parseIgnore(g.rel(dir), string(body)))
	return true
}

func (g *ignores) leave() { g.files = g.files[:len(g.files)-1] }

func (g *ignores) rel(path string) string {
	rel, _ := filepath.Rel(g.top, path)
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// ignored applies the deepest file first and, within a file, its last matching line, as git does.
func (g *ignores) ignored(path string, dir bool) bool {
	if g == nil {
		return false
	}
	rel := g.rel(path)
	for i := len(g.files) - 1; i >= 0; i-- {
		f := g.files[i]
		sub := rel
		if f.base != "" {
			var ok bool
			if sub, ok = strings.CutPrefix(rel, f.base+"/"); !ok {
				continue
			}
		}
		for j := len(f.rules) - 1; j >= 0; j-- {
			if r := f.rules[j]; (dir || !r.dirOnly) && r.re.MatchString(sub) {
				return !r.negate
			}
		}
	}
	return false
}
