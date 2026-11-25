package app

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// A person chooses the folder a workspace is by browsing the machine of
// its environment, wherever that machine is.

// Folder is a folder of an environment's machine and the folders in it,
// by name, those whose names start with a dot left out. Parent is empty
// at the root.
type Folder struct {
	Path    string
	Parent  string
	Folders []string
}

// ListFolders is a folder of an environment's machine: path, absolute, or
// the environment's own folder when empty.
func (a *App) ListFolders(ctx context.Context, user, environment, path string) (Folder, error) {
	env, err := a.environment(ctx, user, environment)
	if err != nil {
		return Folder{}, err
	}
	box, err := a.envs.In(ctx, user, env.ID)
	if err != nil {
		return Folder{}, err
	}
	if path == "" {
		roots := box.Roots()
		if len(roots) == 0 {
			return Folder{}, fmt.Errorf("environment %s names no folder of its own", env.Name)
		}
		path = roots[0]
	}
	if !filepath.IsAbs(path) {
		return Folder{}, fmt.Errorf("%w: %q is not an absolute path", ErrInvalidPath, path)
	}
	dir, err := box.Directory(ctx, path)
	if err != nil {
		return Folder{}, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	entries, err := box.ReadDir(ctx, dir)
	if err != nil {
		return Folder{}, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	f := Folder{Path: dir, Folders: []string{}}
	if parent := filepath.Dir(dir); parent != dir {
		f.Parent = parent
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		folder := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			st, err := box.Stat(ctx, filepath.Join(dir, e.Name()))
			folder = err == nil && st.IsDir()
		}
		if folder {
			f.Folders = append(f.Folders, e.Name())
		}
	}
	slices.SortFunc(f.Folders, func(x, y string) int { return naturalCompare(strings.ToLower(x), strings.ToLower(y)) })
	return f, nil
}

// MakeFolder makes a folder named name in parent, a folder of an
// environment's machine, and answers its path.
func (a *App) MakeFolder(ctx context.Context, user, environment, parent, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("%w: %q is not a folder name", ErrInvalidPath, name)
	}
	env, err := a.environment(ctx, user, environment)
	if err != nil {
		return "", err
	}
	box, err := a.envs.In(ctx, user, env.ID)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(parent) {
		return "", fmt.Errorf("%w: %q is not an absolute path", ErrInvalidPath, parent)
	}
	dir, err := box.Directory(ctx, parent)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	path := filepath.Join(dir, name)
	if err := box.MakeDir(ctx, path); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	return path, nil
}
