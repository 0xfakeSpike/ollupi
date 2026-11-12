package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// A skill's resource stays inside its folder: no path, link or parent reaches a file beside it.
func TestAResourceStaysInsideItsSkill(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "skill")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{filepath.Join(root, "docs", "guide.md"): "guide", filepath.Join(base, "secret.txt"): "secret"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(root, "docs", "link.md")); err != nil {
		t.Fatal(err)
	}
	if got, err := safeResourcePath(root, "docs/guide.md"); err != nil || got != filepath.Join(root, "docs", "guide.md") {
		t.Errorf("a file in the skill: %q %v", got, err)
	}
	for _, bad := range []string{"../secret.txt", "docs/../../secret.txt", filepath.Join(base, "secret.txt"), "docs/link.md", "..", "."} {
		if got, err := safeResourcePath(root, bad); err == nil {
			t.Errorf("%s reached %s", bad, got)
		}
	}
}
