package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
)

// dirs is what a call works on: the caller's box, and the roots a command may write in it; it runs in the first.
type dirs struct {
	box   sandbox.Box
	roots []string
}

func (r *Provider) dirs(ctx context.Context) (dirs, error) {
	caller := tools.CallerOf(ctx)
	box := r.box
	if r.boxFor != nil {
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

func (r *Provider) settingsDigest(d dirs) string {
	return digest(struct {
		Sandbox    any
		TimeoutSec int
		Blacklist  []string
		Roots      []string
	}{d.box.Fingerprint(), r.timeoutSec, r.blacklist, d.roots})
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digest(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("workspace: digest non-JSON value: %v", err))
	}
	return sha(string(body))
}
