package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
)

var errStopWalk = errors.New("stop walk")

type walker struct {
	box       sandbox.Box
	root      string
	ignores   *ignores
	entries   int
	skipped   int
	truncated bool
	reaches   func(abs string) bool
}

func newWalker(ctx context.Context, box sandbox.Box, root string) *walker {
	return &walker{box: box, root: root, ignores: gitIgnores(ctx, box, root)}
}

func (w *walker) walk(ctx context.Context, visit func(abs string, info fs.FileInfo) error) error {
	st, err := w.box.Stat(ctx, w.root)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("workspace: %s is not a folder", w.root)
	}
	return w.dir(ctx, w.root, visit)
}

func (w *walker) dir(ctx context.Context, dir string, visit func(string, fs.FileInfo) error) error {
	if w.ignores != nil && w.ignores.enter(ctx, dir) {
		defer w.ignores.leave()
	}
	entries, err := w.box.ReadDir(ctx, dir)
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
		info, ok := w.entry(ctx, abs, e)
		if !ok || w.ignores.ignored(abs, info.IsDir()) {
			continue
		}
		var err error
		if info.IsDir() {
			unreachable := w.reaches != nil && !w.reaches(abs)
			if unreachable {
				continue
			}
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

func (w *walker) entry(ctx context.Context, abs string, e fs.DirEntry) (fs.FileInfo, bool) {
	if w.box.CheckRead(abs) != nil {
		w.skipped++
		return nil, false
	}
	info, err := e.Info()
	if err != nil {
		w.skipped++
		return nil, false
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return info, true
	}
	target, err := w.box.Canonical(ctx, abs)
	if err != nil {
		return nil, false
	}
	if w.box.CheckRead(target) != nil {
		w.skipped++
		return nil, false
	}
	if info, err = w.box.Stat(ctx, target); err != nil || info.IsDir() {
		return nil, false
	}
	return info, true
}
