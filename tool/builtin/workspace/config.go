package workspace

import "fmt"

// Config is how commands run in any environment: their timeouts and the
// commands never run. Where they run, and within what limits, is the
// environment's sandbox (sandbox.Provider).
type Config struct {
	CommandTimeoutSec    int      `json:"command_timeout_sec"`
	MaxCommandTimeoutSec int      `json:"max_command_timeout_sec,omitempty"`
	CommandBlacklist     []string `json:"command_blacklist"`
}

func (c *Config) normalize() error {
	if c.CommandTimeoutSec <= 0 {
		c.CommandTimeoutSec = 30
	}
	if c.MaxCommandTimeoutSec < 0 {
		return fmt.Errorf("max_command_timeout_sec must be >= 0")
	}
	if c.MaxCommandTimeoutSec == 0 {
		c.MaxCommandTimeoutSec = max(600, c.CommandTimeoutSec)
	}
	if c.MaxCommandTimeoutSec < c.CommandTimeoutSec {
		return fmt.Errorf("max_command_timeout_sec must be >= command_timeout_sec")
	}
	return nil
}
