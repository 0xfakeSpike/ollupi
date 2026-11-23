package sqlite

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
)

// homeRoot and homePrefix begin a workspace root the store keeps relative to
// the user's home, as a shell's ~ does: the row names the same folder on
// another machine whatever its user is called, so copying the store over is
// enough to carry the workspaces with it.
const (
	homeRoot   = "~"
	homePrefix = homeRoot + "/"
)

// userHome is the base workspace roots are kept relative to; empty on a
// machine without a home directory, which leaves every root absolute.
func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Clean(home)
}

// storeRoot is root as the store keeps it: under base it is written with
// homePrefix. A folder outside base stays absolute: no copy of the store can
// name it on another machine anyway.
func storeRoot(root, base string) string {
	if base == "" || root == "" || !filepath.IsAbs(root) {
		return root
	}
	rel, err := filepath.Rel(base, root)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return root
	}
	if rel == "." {
		return homeRoot
	}
	return homePrefix + filepath.ToSlash(rel)
}

// readRoot is the folder storeRoot kept, on this machine.
func readRoot(root, base string) string {
	if base == "" {
		return root
	}
	if root == homeRoot {
		return base
	}
	if !strings.HasPrefix(root, homePrefix) {
		return root
	}
	return filepath.Join(base, filepath.FromSlash(strings.TrimPrefix(root, homePrefix)))
}

// normalizeRoots rewrites the absolute roots an older version wrote to the
// relative form, so an old store copied over still names its workspaces here.
// A rewrite that would take another row's root is skipped: two equal roots
// would break the table's uniqueness, and the absolute row still names this
// machine's folder.
func normalizeRoots(g *gorm.DB, base string) error {
	if base == "" {
		return nil
	}
	var rows []struct {
		ID   string
		Root string
	}
	if err := g.Raw("SELECT id, root FROM workspaces").Scan(&rows).Error; err != nil {
		return fmt.Errorf("normalize workspace roots: %w", err)
	}
	taken := make(map[string]bool, len(rows))
	for _, row := range rows {
		taken[row.Root] = true
	}
	return g.Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			stored := storeRoot(row.Root, base)
			if stored == row.Root || taken[stored] {
				continue
			}
			if err := tx.Exec("UPDATE workspaces SET root = ? WHERE id = ?", stored, row.ID).Error; err != nil {
				return fmt.Errorf("normalize workspace %s: %w", row.ID, err)
			}
			taken[stored] = true
		}
		return nil
	})
}
