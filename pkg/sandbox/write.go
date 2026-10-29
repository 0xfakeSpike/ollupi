package sandbox

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// CheckWrite applies the sandbox's write rules to one file the agent writes itself, without running a command:
// it must be inside one of roots, and when the sandbox is enforced, outside protected, read-only and credential paths.
func (s *Sandbox) CheckWrite(roots []string, path string) error {
	if s.policy.Mode == ModeReadOnly {
		return fmt.Errorf("sandbox: files are read-only")
	}
	i := slices.IndexFunc(roots, func(r string) bool { return Within(r, path) })
	if i < 0 {
		return fmt.Errorf("sandbox: %q is outside the command directories", path)
	}
	if !s.Enforced() {
		return nil
	}
	for _, dir := range slices.Concat(s.policy.DenyRead, s.policy.ReadOnly) {
		if Within(dir, path) {
			return fmt.Errorf("sandbox: %q is protected and cannot be written", path)
		}
	}
	rel, err := filepath.Rel(roots[i], path)
	if err != nil {
		return err
	}
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if part == ".git" || slices.Contains(s.policy.ProtectedNames, part) {
			return fmt.Errorf("sandbox: %q is protected (%s) and cannot be written", path, part)
		}
	}
	return nil
}
