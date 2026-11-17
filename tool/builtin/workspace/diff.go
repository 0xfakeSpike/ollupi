package workspace

import (
	"fmt"

	udiff "github.com/aymanbagabas/go-udiff"
)

const diffContext = 3

// The diff need not be the shortest: its work is bounded, so a file rewritten whole costs no more than a few
// passes over it.
func unifiedDiff(path, before, after string, created bool) (string, int, int, error) {
	from := "a/" + path
	if created {
		from = "/dev/null"
	}
	u, err := udiff.ToUnifiedDiff(from, "b/"+path, before, udiff.Lines(before, after), diffContext)
	if err != nil {
		return "", 0, 0, fmt.Errorf("workspace: diff %s: %w", path, err)
	}
	added, removed := 0, 0
	for _, h := range u.Hunks {
		for _, l := range h.Lines {
			switch l.Kind {
			case udiff.Insert:
				added++
			case udiff.Delete:
				removed++
			}
		}
	}
	return u.String(), added, removed, nil
}
