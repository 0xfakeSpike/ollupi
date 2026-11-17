package sandbox

import "fmt"

// CheckRead holds in every mode: the agent's own reads run without asking the user, so credential and data paths stay
// closed even with the sandbox off. path must be canonical, so a link cannot lead around it.
func (s *Sandbox) CheckRead(path string) error {
	for _, dir := range s.policy.DenyRead {
		if within(dir, path) {
			return fmt.Errorf("sandbox: %q cannot be read", path)
		}
	}
	return nil
}
