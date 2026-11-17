package sandbox

import "fmt"

func (s *Sandbox) CheckRead(path string) error {
	for _, dir := range s.policy.DenyRead {
		if within(dir, path) {
			return fmt.Errorf("sandbox: %q cannot be read", path)
		}
	}
	return nil
}
