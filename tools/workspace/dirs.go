package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
)

// dirs holds the roots a command may write; it runs in the first.
type dirs struct {
	roots []string
}

func (r *Provider) dirs(caller tools.Caller) (dirs, error) {
	if caller.Root == "" {
		return dirs{roots: r.box.Roots()}, nil
	}
	root, err := sandbox.CanonicalDirectory(caller.Root)
	if err != nil {
		return dirs{}, err
	}
	if root != caller.Root {
		return dirs{}, fmt.Errorf("workspace: the project directory %q now resolves to %q; add its new location as a workspace", caller.Root, root)
	}
	if err := r.box.Check(root); err != nil {
		return dirs{}, err
	}
	return dirs{roots: []string{root}}, nil
}

func (d dirs) resolve(requested string) (string, error) {
	path := requested
	if path == "" {
		path = d.roots[0]
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(d.roots[0], path)
	}
	resolved, err := sandbox.CanonicalDirectory(path)
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
	}{r.box.Fingerprint(), r.timeoutSec, r.blacklist, d.roots})
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
