package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
)

// dirs is what a call works on: the caller's box, and the roots a command may write in it; it runs in the first.
type dirs struct {
	box   sandbox.Box
	roots []string
}

func (r *Provider) dirs(ctx context.Context, caller tools.Caller) (dirs, error) {
	box := r.box
	switch {
	case r.boxIn != nil:
		var err error
		if box, err = r.boxIn(caller); err != nil {
			return dirs{}, fmt.Errorf("workspace: the environment of the conversation: %w", err)
		}
		if box == nil {
			box = r.box
		}
	case r.boxFor != nil:
		var err error
		if box, err = r.boxFor(caller.UserID); err != nil {
			return dirs{}, fmt.Errorf("workspace: the sandbox of the user: %w", err)
		}
	}
	if caller.Root == "" {
		return dirs{box: box, roots: box.Roots()}, nil
	}
	root, err := box.Directory(ctx, caller.Root)
	if err != nil {
		return dirs{}, err
	}
	if root != caller.Root {
		return dirs{}, fmt.Errorf("workspace: the project directory %q now resolves to %q; add its new location as a workspace", caller.Root, root)
	}
	if err := box.Check(root); err != nil {
		return dirs{}, err
	}
	return dirs{box: box, roots: []string{root}}, nil
}

func (d dirs) resolve(ctx context.Context, requested string) (string, error) {
	path := requested
	if path == "" {
		path = d.roots[0]
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(d.roots[0], path)
	}
	resolved, err := d.box.Directory(ctx, path)
	if err != nil {
		return "", err
	}
	for _, root := range d.roots {
		if sandbox.Within(root, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("workspace: directory %q is outside the command directories", requested)
}

// workDir is where a command runs: the work_dir it asks for, or the first root.
func (d dirs) workDir(ctx context.Context, args map[string]any) (string, error) {
	requested := ""
	if raw, ok := args["work_dir"]; ok {
		requested, _ = raw.(string)
		if strings.TrimSpace(requested) == "" {
			return "", fmt.Errorf("workspace: argument work_dir must be a non-empty string")
		}
	}
	return d.resolve(ctx, requested)
}
