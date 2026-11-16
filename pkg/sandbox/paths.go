package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func CanonicalPath(path, home string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("sandbox: empty path")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home == "" {
			return "", fmt.Errorf("sandbox: %q needs a home directory", path)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	existing, rest := abs, ""
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			return filepath.Join(resolved, rest), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("sandbox: resolve %q: %w", abs, err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
}

func CanonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("directory is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("directory %q is unavailable: %w", abs, err)
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("directory %q is unavailable: %w", resolved, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%q is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

// caselessNames says this machine's filesystems match names in any case, as they do by default on macOS and Windows: a
// rule on ~/.ssh must then hold for ~/.SSH too, as the seatbelt profile's rules do.
var caselessNames = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// within is Within for this machine's paths.
func within(root, path string) bool {
	if caselessNames {
		root, path = strings.ToLower(root), strings.ToLower(path)
	}
	return Within(root, path)
}

func Within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
