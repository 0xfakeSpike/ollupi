package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

const maxIgnoreBytes = 1 << 20

func checkPattern(p string) (string, error) {
	p = strings.TrimPrefix(p, "./")
	if !doublestar.ValidatePattern(p) {
		return "", fmt.Errorf("pattern %q is not a valid glob", p)
	}
	return p, nil
}

func parseIgnore(base, text string) []gitignore.Pattern {
	var domain []string
	if base != "" {
		domain = strings.Split(base, "/")
	}
	var out []gitignore.Pattern
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "#") && strings.TrimSpace(line) != "" {
			out = append(out, gitignore.ParsePattern(line, domain))
		}
	}
	return out
}

type ignores struct {
	box      sandbox.Box
	top      string
	patterns []gitignore.Pattern
	added    []int
}

func gitIgnores(ctx context.Context, box sandbox.Box, dir string) *ignores {
	top := dir
	for {
		if _, err := box.Stat(ctx, filepath.Join(top, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(top)
		if parent == top {
			return nil
		}
		top = parent
	}
	g := &ignores{box: box, top: top}
	if body, err := sandbox.ReadFile(ctx, box, filepath.Join(top, ".git", "info", "exclude"), maxIgnoreBytes); err == nil {
		g.patterns = parseIgnore("", string(body))
	}
	for at := top; at != dir; {
		g.enter(ctx, at)
		rel, _ := filepath.Rel(at, dir)
		at = filepath.Join(at, strings.SplitN(rel, string(filepath.Separator), 2)[0])
	}
	return g
}

func (g *ignores) enter(ctx context.Context, dir string) bool {
	body, err := sandbox.ReadFile(ctx, g.box, filepath.Join(dir, ".gitignore"), maxIgnoreBytes)
	if err != nil {
		return false
	}
	more := parseIgnore(g.rel(dir), string(body))
	g.patterns, g.added = append(g.patterns, more...), append(g.added, len(more))
	return true
}

func (g *ignores) leave() {
	n := g.added[len(g.added)-1]
	g.patterns, g.added = g.patterns[:len(g.patterns)-n], g.added[:len(g.added)-1]
}

func (g *ignores) rel(path string) string {
	rel, _ := filepath.Rel(g.top, path)
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (g *ignores) ignored(path string, dir bool) bool {
	if g == nil {
		return false
	}
	return gitignore.NewMatcher(g.patterns).Match(strings.Split(g.rel(path), "/"), dir)
}
