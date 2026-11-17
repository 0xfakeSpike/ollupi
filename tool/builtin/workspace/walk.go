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
		if w.box.CheckRead(abs) != nil {
			w.skipped++
			continue
		}
		info, err := e.Info()
		if err != nil {
			w.skipped++
			continue
		}
		isDir := info.IsDir()
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := w.box.Canonical(ctx, abs)
			if err != nil {
				continue
			}
			if w.box.CheckRead(target) != nil {
				w.skipped++
				continue
			}
			if info, err = w.box.Stat(ctx, target); err != nil || info.IsDir() {
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
