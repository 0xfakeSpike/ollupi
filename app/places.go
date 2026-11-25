package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/store"
)

// place is a workspace as a person reaches its files: the box of its
// environment and the folder that is its root there. A scratch workspace's
// folder is the environment's own, as its box names it.
type place struct {
	workspace account.Workspace
	box       sandbox.Box
	root      string
}

func (a *App) place(ctx context.Context, user, workspace string) (place, error) {
	var w account.Workspace
	if err := a.st.Read(ctx, func(r store.Reader) (err error) { w, err = r.Workspace(user, workspace); return err }); err != nil {
		return place{}, err
	}
	box, err := a.envs.In(ctx, user, w.Environment)
	if err != nil {
		return place{}, err
	}
	root := w.Root
	if w.Scratch() {
		roots := box.Roots()
		if len(roots) == 0 {
			return place{}, fmt.Errorf("the environment of workspace %s names no folder of its own", w.Name)
		}
		root = roots[0]
	}
	if root, err = box.Directory(ctx, root); err != nil {
		return place{}, fmt.Errorf("the folder of workspace %s: %w", w.Name, err)
	}
	return place{workspace: w, box: box, root: root}, nil
}

// path is rel, a slash-separated path relative to the root, in the box;
// one that leaves the workspace, through a symlink too, is refused. An
// empty rel is the root.
func (p place) path(ctx context.Context, rel string) (string, error) {
	local := filepath.FromSlash(rel)
	if local != "" && !filepath.IsLocal(local) {
		return "", fmt.Errorf("%w: %q is not a path inside the workspace", ErrInvalidPath, rel)
	}
	abs, err := p.box.Canonical(ctx, filepath.Join(p.root, local))
	if err != nil {
		return "", err
	}
	if !sandbox.Within(p.root, abs) {
		return "", fmt.Errorf("%w: %q leads out of the workspace", ErrInvalidPath, rel)
	}
	return abs, nil
}

// relative is abs, a path in the workspace, as a slash-separated path
// relative to its root.
func (p place) relative(abs string) string {
	rel, err := filepath.Rel(p.root, abs)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(strings.TrimPrefix(rel, "./"))
}
