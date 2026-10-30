package sandbox

import "fmt"

// CheckRead applies the sandbox's read rules to a path the agent reads itself, without running a command. It
// holds in every mode: those reads run without asking the user, so credential and data paths stay closed to
// them even with the sandbox off. path must be canonical (CanonicalPath), so a link cannot lead around it.
func (s *Sandbox) CheckRead(path string) error {
	for _, dir := range s.policy.DenyRead {
		if Within(dir, path) {
			return fmt.Errorf("sandbox: %q cannot be read", path)
		}
	}
	return nil
}
